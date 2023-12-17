package main

/*
1. ve-ps-launcher.exe -> Metaverse.exe -AppID=12382 -WorldID=23131 -psport=8888 ... -> goroutine
2. ve-ps-launcher.exe -> goroutine -> heartbeat, sleep(60sec) -> id, status (offline, occupied, free)
3. which instances are running and busy|free?
table: pixel_streaming_instances
id, release_id, region_id, host, port, status, instance_id
table: pixel_streaming_sessions
id, created_at, updated_at, instance_id, app_id, world_id, status

ps_instance::status [ 'offline', 'online', 'stopped' ]
ps_session::status [ 'offline', 'occupied', 'free' ]

ve-ps-launcher.exe -> (only on-demand instances) when user closes connection -> close app, delete session file, start app

ve-ps-operator.exe -> once in minute get all free sessions, check if some sessions can be closed -> close sessions

ve-ps-launcher.exe -> refresh session metadata -> if session is closed -> close app -> clear all user data -> wait for demand of a new user

Tables:
PS Instance (pixel_streaming_instance)
PS Session (pixel_streaming_session)

Entities:
Operator - single orchestrating go service that manages instances (start, stop, delete)
Instance - machine with GPU that hosts launcher
Launcher - go service started as an entrypoint on each instance, manages the locally running app on the instance
Session - request from the end user to play the game in PS mode, launcher is assigned to each session and starts required game app with required params

Instance Statuses:
- Deleted - deleted (on demand or spot) (Terminated instance status)
Stopped - stopped (on demand only) (Stopped instance status)
- Pending - pending to be Free (pending to make instance)
- Free - ready to be used (its launcher is waiting for the session) (Starting instance status)
Occupied - launcher is running the game, a user is connected (Starting instance status)

Session Statuses:
Pending - waiting for any launcher to catch up the session and start game
Starting - waiting for the assigned launcher to prepare and start the game
Running - launcher is running the game, a user is connected
Closed - user has disconnected, session has been closed, all user data has been purged

Operator manages instances to have F=1 (where F is configuration variable) Free instances available. It does checks each T=60 seconds (where T is configuration variable). During each check it decides if it needs some instances to be removed (actual free number > F) and some to be kept (Running or actual free number = F) or start new (actual free number < F) to maintain the F number.

Launcher does not know anything about the machine it is running at, the machine can go down any time. Launcher is always running inside the instance. Each T seconds it checks if any app session should be started, and assigns itself to a such Pending session. The session receives the Starting status while the launcher is preparing the session desired app and world game files. When required game files are ready, then launcher starts the game itself with required arguments. It keeps checking the session via the API and if session becomes Closed it shuts the app subprocess and cleans up any user data. And returns to the waiting for a session state.
*/

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/sirupsen/logrus"
	"golang.org/x/exp/slices"
	"os"
	"veverse-pixelstreaming-operator/reflect"
)

const (
	PS_STATUS_CREATED       = "created"
	PS_STATUS_PENDING       = "pending"
	PS_STATUS_RUNNING       = "running"
	PS_STATUS_STOPPING      = "stopping"
	PS_STATUS_STOPPED       = "stopped"
	PS_STATUS_SHUTTING_DOWN = "shutting-down"
	PS_STATUS_TERMINATED    = "terminated"

	FREE_SPOTS_AVAILABLE        int32 = 1
	FREE_ON_DEMAND_AVAILABLE    int32 = 1
	STOPPED_ON_DEMAND_AVAILABLE int32 = 1

	AMI_WINDOWS = "ami-xxxxxxxxxxxxxxxxx"
	AMI_LINUX   = "ami-xxxxxxxxxxxxxxxxx"

	WIN_SPOT_LAUNCH_TEMPLATE_ID      = "lt-xxxxxxxxxxxxxxxxx"
	WIN_ON_DEMAND_LAUNCH_TEMPLATE_ID = "lt-xxxxxxxxxxxxxxxxx"
)

var (
	PSInstanceSingular = "PixelStreamingInstance"
	PSInstancePlural   = "PixelStreamingInstances"

	RegionSingular = "Region"
	RegionPlural   = "Regions"
)

var userData = ``

var encodedUserData = base64.StdEncoding.EncodeToString([]byte(userData))

var (
	AwsAccessKey   = os.Getenv("AWS_ACCESS_KEY")
	AwsSecretKey   = os.Getenv("AWS_SECRET_KEY")
	AMIID          = "ami-xxxxxxxxxxxxxxxxx"
	keyPair        = "PixelStreamingOpenSSH"
	subnetId       = "subnet-xxxxxxxxxxxxxxxxx"
	securityGroups = []string{
		"sg-xxxxxxxxxxxxxxxxx",
	}

	cfg aws.Config

	stopInstanceInput = &ec2.StopInstancesInput{
		InstanceIds: nil,
		DryRun:      nil,
		Force:       nil,
		Hibernate:   nil,
	}

	terminateInstanceInput = &ec2.TerminateInstancesInput{
		InstanceIds: nil,
		DryRun:      nil,
	}

	makeSpotInstanceInput = &ec2.RunInstancesInput{
		ImageId: aws.String(AMI_WINDOWS),
		LaunchTemplate: &types.LaunchTemplateSpecification{
			LaunchTemplateId: aws.String(WIN_SPOT_LAUNCH_TEMPLATE_ID),
		},
		InstanceType: types.InstanceTypeG5Xlarge,
		KeyName:      aws.String(keyPair),
		MaxCount:     aws.Int32(1),
		MinCount:     aws.Int32(1),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeInstance,
				Tags: []types.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String("VeVerse-PixelStreaming-Windows-Spot"),
					},
				},
			},
		},
		//UserData: aws.String(encodedUserData),
	}

	makeOnDemandInstanceInput = &ec2.RunInstancesInput{
		ImageId: aws.String(AMI_WINDOWS),
		LaunchTemplate: &types.LaunchTemplateSpecification{
			LaunchTemplateId: aws.String(WIN_ON_DEMAND_LAUNCH_TEMPLATE_ID),
		},
		InstanceType: types.InstanceTypeG5Xlarge,
		KeyName:      aws.String(keyPair),
		MaxCount:     aws.Int32(1),
		MinCount:     aws.Int32(1),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeInstance,
				Tags: []types.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String("VeVerse-PixelStreaming-Windows-OnDemand"),
					},
				},
			},
		},
	}
)

func CheckAvailabilitySpotInstance(ctx context.Context) (err error) {
	db, ok := ctx.Value("database").(*pgxpool.Pool)
	if !ok {
		return fmt.Errorf("unable to get database connection")
	}

	var regions map[uuid.UUID]string
	regions, err = GetRegions(ctx)

	for regionId, regionName := range regions {
		var (
			totalAvailableInstance int32 = 0
			totalFreeInstance      int32 = 0
		)

		q := `SELECT COUNT(psi.id) total, count(psi2.id) as total_free FROM pixel_streaming_instance psi
LEFT JOIN pixel_streaming_instance psi2 ON 
    psi.id = psi2.id AND
	psi.status IN('free', 'pending') AND
	psi2.status = 'free' AND
    psi2.region_id = psi.region_id AND
    psi.region_id = $1
WHERE psi.instance_type = 'spot'`

		row := db.QueryRow(ctx, q, regionId)

		err = row.Scan(&totalAvailableInstance, &totalFreeInstance)
		if err != nil {
			logrus.Errorf("failed to scan %s @ %s: %v", PSInstanceSingular, reflect.FunctionName(), err)
			return fmt.Errorf("failed to scan ps instances total %s", PSInstanceSingular)
		}

		cfg, err = config.LoadDefaultConfig(
			ctx,
			config.WithRegion(regionName),
			config.WithClientLogMode(aws.LogRequestWithBody),
			config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(AwsAccessKey, AwsSecretKey, "")),
		)

		if err != nil {
			return fmt.Errorf("failed to load aws config: %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
		}

		var (
			runInstanceOutput *ec2.RunInstancesOutput
			getInstanceOutput *ec2.DescribeInstancesOutput
			ec2Client         = ec2.NewFromConfig(cfg)
		)

		describeInstanceInput := ec2.DescribeInstancesInput{
			DryRun: nil,
			Filters: []types.Filter{
				{
					Name:   aws.String("image-id"),
					Values: []string{AMI_WINDOWS},
				},
				NewEC2Filter("tag:aws:ec2launchtemplate:id", WIN_SPOT_LAUNCH_TEMPLATE_ID),
				NewEC2Filter("instance-state-name", PS_STATUS_RUNNING, PS_STATUS_PENDING),
			},
		}

		getInstanceOutput, _ = GetInstances(ctx, ec2Client, &describeInstanceInput)

		var reservedInstances []types.Instance
		if getInstanceOutput != nil && len(getInstanceOutput.Reservations) > 0 {
			reservedInstances = getInstanceOutput.Reservations[0].Instances
		}

		var availableSpotInstancesCount = CountAWSInstancesByState(reservedInstances, PS_STATUS_RUNNING, PS_STATUS_PENDING)
		var totalPendingInstance = totalAvailableInstance - totalFreeInstance
		if totalFreeInstance > FREE_SPOTS_AVAILABLE {
			// To be terminated
			var (
				freeInstanceIds      []string
				pendingInstanceUUIDs []uuid.UUID
			)

			fmt.Println("TO BE TERMINATED", freeInstanceIds, pendingInstanceUUIDs)
			freeInstanceIds, pendingInstanceUUIDs, err = GetInstanceIds(ctx, regionId, "spot", "free", "pending")
			var terminateCount = totalFreeInstance - FREE_SPOTS_AVAILABLE
			freeInstanceIds = freeInstanceIds[0:terminateCount]
			err = TerminateInstances(ctx, ec2Client, freeInstanceIds)

			if err != nil {
				return fmt.Errorf("failed to terminate: %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
			} else {
				data := PixelStreamingInstanceMetadata{
					Status: aws.String("deleted"),
				}

				for _, id := range freeInstanceIds {
					data.InstanceId = &id
					err = UpdatePixelStreamingInstance(ctx, nil, data)
					if err != nil {
						return fmt.Errorf("failed to update running instance data: %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
					}
				}
			}
		} else if availableSpotInstancesCount > 0 && availableSpotInstancesCount >= totalPendingInstance {
			// update
			var (
				freeInstanceIds      []string
				pendingInstanceUUIDs []uuid.UUID
			)

			freeInstanceIds, pendingInstanceUUIDs, err = GetInstanceIds(ctx, regionId, "spot", "free", "pending")
			for _, instance := range reservedInstances {
				if instance.InstanceId != nil && instance.PublicIpAddress != nil && instance.State.Name == "running" {

					if slices.Contains(freeInstanceIds, *instance.InstanceId) {
						continue
					}

					data := PixelStreamingInstanceMetadata{
						InstanceId: instance.InstanceId,
						Status:     aws.String("free"),
						Host:       instance.PublicIpAddress,
					}

					var updateID uuid.UUID
					updateID, pendingInstanceUUIDs = pendingInstanceUUIDs[0], pendingInstanceUUIDs[1:]
					err = UpdatePixelStreamingInstance(ctx, &updateID, data)
					if err != nil {
						return fmt.Errorf("failed to update running instance data: %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
					}
				}
			}
		} else {
			var makingInstanceCount = int(FREE_SPOTS_AVAILABLE - totalAvailableInstance)
			if makingInstanceCount > 0 {
				q := `INSERT INTO pixel_streaming_instance (id, region_id, port, instance_type, status) VALUES (
					$1, $2, $3, $4, $5                                                 
				)`

				for i := 0; i < makingInstanceCount; i++ {
					id, err1 := uuid.NewV4()
					if err1 != nil {
						logrus.Errorf("failed to generate uuid %s @ %s: %v", PSInstanceSingular, reflect.FunctionName(), err)
						return fmt.Errorf("failed to set %s", PSInstanceSingular)
					}

					data := PixelStreamingInstanceMetadata{
						Id:           &id,
						RegionId:     &regionId,
						Port:         aws.Uint16(80),
						InstanceType: aws.String("spot"),
						Status:       aws.String("pending"),
					}

					_, err = db.Exec(ctx, q, data.Id, data.RegionId, data.Port, data.InstanceType, data.Status)
					if err != nil {
						logrus.Errorf("failed to insert uuid %s @ %s: %v", PSInstanceSingular, reflect.FunctionName(), err)
						return fmt.Errorf("failed to set %s", PSInstanceSingular)
					}
				}
			} else if makingInstanceCount == 0 {
				makeSpotInstanceInput.MaxCount = aws.Int32(totalAvailableInstance - totalFreeInstance)

				runInstanceOutput, err = MakeInstance(ctx, ec2Client, makeSpotInstanceInput)
				if err != nil {
					return fmt.Errorf("failed to create spot instance: %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
				}

				logrus.Infof("Making required free spot instances: %v", runInstanceOutput.Instances)
			}
		}
	}

	return err
}

func CheckAvailabilityOnDemandInstance(ctx context.Context) (err error) {
	db, ok := ctx.Value("database").(*pgxpool.Pool)
	if !ok {
		return fmt.Errorf("unable to get database connection")
	}

	var regions map[uuid.UUID]string
	regions, err = GetRegions(ctx)

	for regionId, regionName := range regions {
		var (
			totalAvailableInstance int32 = 0
			totalFreeInstance      int32 = 0
			totalStoppedInstance   int32 = 0
		)

		q := `SELECT COUNT(psi.id) total, count(psi2.id) as total_free, count(psi3.id) as total_stopped FROM pixel_streaming_instance psi
LEFT JOIN pixel_streaming_instance psi2 ON
    psi.id = psi2.id AND
	psi.status IN('free', 'pending', 'stopped') AND
	psi2.status IN ('free') AND
    psi2.region_id = psi.region_id AND
    psi2.region_id = $1
LEFT JOIN pixel_streaming_instance psi3 ON
    psi.id = psi3.id AND
	psi3.status IN ('stopped') AND
	psi3.region_id = psi.region_id AND
    psi3.region_id = $1
    WHERE psi.instance_type = 'on-demand'`

		row := db.QueryRow(ctx, q, regionId)

		err = row.Scan(&totalAvailableInstance, &totalFreeInstance, &totalStoppedInstance)
		if err != nil {
			logrus.Errorf("failed to scan %s @ %s: %v", PSInstanceSingular, reflect.FunctionName(), err)
			return fmt.Errorf("failed to scan ps instances total %s", PSInstanceSingular)
		}

		cfg, err = config.LoadDefaultConfig(
			ctx,
			config.WithRegion(regionName),
			config.WithClientLogMode(aws.LogRequestWithBody),
			config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(AwsAccessKey, AwsSecretKey, "")),
		)

		if err != nil {
			return fmt.Errorf("failed to load aws config: %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
		}

		var (
			runInstanceOutput *ec2.RunInstancesOutput
			getInstanceOutput *ec2.DescribeInstancesOutput
			ec2Client         = ec2.NewFromConfig(cfg)
		)

		describeInstanceInput := ec2.DescribeInstancesInput{
			DryRun: nil,
			Filters: []types.Filter{
				{
					Name:   aws.String("image-id"),
					Values: []string{AMI_WINDOWS},
				},
				NewEC2Filter("tag:aws:ec2launchtemplate:id", WIN_ON_DEMAND_LAUNCH_TEMPLATE_ID),
				NewEC2Filter("instance-state-name", PS_STATUS_RUNNING, PS_STATUS_PENDING, PS_STATUS_STOPPING, PS_STATUS_STOPPED),
			},
		}

		getInstanceOutput, _ = GetInstances(ctx, ec2Client, &describeInstanceInput)

		var reservedInstances []types.Instance
		if getInstanceOutput != nil && len(getInstanceOutput.Reservations) > 0 {
			for _, reservation := range getInstanceOutput.Reservations {
				reservedInstances = append(reservedInstances, reservation.Instances...)
			}
		}

		var availableOnDemandInstancesCount = CountAWSInstancesByState(reservedInstances, PS_STATUS_RUNNING, PS_STATUS_PENDING, PS_STATUS_STOPPING, PS_STATUS_STOPPED)
		var totalPendingInstance = totalAvailableInstance - totalFreeInstance - totalStoppedInstance
		if totalFreeInstance > FREE_ON_DEMAND_AVAILABLE {
			var (
				freeInstanceIds      []string
				pendingInstanceUUIDs []uuid.UUID
			)

			freeInstanceIds, pendingInstanceUUIDs, err = GetInstanceIds(ctx, regionId, "on-demand", "free", "pending")
			fmt.Println("TO BE TERMINATED", freeInstanceIds, pendingInstanceUUIDs)

			var stoppedCount int32 = 0
			var terminateCount = totalFreeInstance - FREE_ON_DEMAND_AVAILABLE
			if totalStoppedInstance < STOPPED_ON_DEMAND_AVAILABLE {
				stoppedCount = STOPPED_ON_DEMAND_AVAILABLE - totalStoppedInstance
				terminateCount = terminateCount - stoppedCount
			}

			var terminateInstanceIds = freeInstanceIds[0:terminateCount]
			if len(terminateInstanceIds) > 0 {
				err = TerminateInstances(ctx, ec2Client, terminateInstanceIds)

				if err != nil {
					return fmt.Errorf("failed to terminate: %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
				} else {
					data := PixelStreamingInstanceMetadata{
						Status: aws.String("deleted"),
					}

					for _, id := range terminateInstanceIds {
						data.InstanceId = &id
						err = UpdatePixelStreamingInstance(ctx, nil, data)
						if err != nil {
							return fmt.Errorf("failed to update running instance data: %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
						}
					}
				}
			}

			var stopInstanceIds = freeInstanceIds[terminateCount : terminateCount+stoppedCount]
			if len(stopInstanceIds) > 0 {
				err = StopInstances(ctx, ec2Client, stopInstanceIds)
				if err != nil {
					return fmt.Errorf("failed to stop on-demand instances: %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
				} else {
					data := PixelStreamingInstanceMetadata{
						Status: aws.String("stopped"),
					}

					for _, id := range stopInstanceIds {
						data.InstanceId = &id
						err = UpdatePixelStreamingInstance(ctx, nil, data)
						if err != nil {
							return fmt.Errorf("failed to update running instance data: %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
						}
					}
				}
			}
		} else if availableOnDemandInstancesCount > 0 && availableOnDemandInstancesCount >= (FREE_ON_DEMAND_AVAILABLE+STOPPED_ON_DEMAND_AVAILABLE) {
			//} else if availableOnDemandInstancesCount > 0 && availableOnDemandInstancesCount >= totalPendingInstance {
			// update
			var (
				freeInstanceIds      []string
				pendingInstanceUUIDs []uuid.UUID
			)

			freeInstanceIds, pendingInstanceUUIDs, err = GetInstanceIds(ctx, regionId, "on-demand", "free", "pending")
			for _, instance := range reservedInstances {
				if instance.InstanceId != nil && instance.PublicIpAddress != nil && instance.State.Name == PS_STATUS_RUNNING {

					if slices.Contains(freeInstanceIds, *instance.InstanceId) {
						continue
					}

					data := PixelStreamingInstanceMetadata{
						InstanceId: instance.InstanceId,
						Status:     aws.String("free"),
						Host:       instance.PublicIpAddress,
					}

					var updateID uuid.UUID

					if len(pendingInstanceUUIDs) > 1 {
						updateID, pendingInstanceUUIDs = pendingInstanceUUIDs[0], pendingInstanceUUIDs[1:]
					} else {
						updateID = pendingInstanceUUIDs[0]
					}
					err = UpdatePixelStreamingInstance(ctx, &updateID, data)
					if err != nil {
						return fmt.Errorf("failed to update running instance data: %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
					}
				}
			}
		} else {
			var makingInstanceCount = int((FREE_ON_DEMAND_AVAILABLE + STOPPED_ON_DEMAND_AVAILABLE) - totalAvailableInstance)
			if makingInstanceCount > 0 {
				q := `INSERT INTO pixel_streaming_instance (id, region_id, port, instance_type, status) VALUES (
					$1, $2, $3, $4, $5                                                 
				)`

				for i := 0; i < makingInstanceCount; i++ {
					id, err1 := uuid.NewV4()
					if err1 != nil {
						logrus.Errorf("failed to generate uuid %s @ %s: %v", PSInstanceSingular, reflect.FunctionName(), err)
						return fmt.Errorf("failed to set %s", PSInstanceSingular)
					}

					data := PixelStreamingInstanceMetadata{
						Id:           &id,
						RegionId:     &regionId,
						Port:         aws.Uint16(80),
						InstanceType: aws.String("on-demand"),
						Status:       aws.String("pending"),
					}

					_, err = db.Exec(ctx, q, data.Id, data.RegionId, data.Port, data.InstanceType, data.Status)
					if err != nil {
						logrus.Errorf("failed to insert uuid %s @ %s: %v", PSInstanceSingular, reflect.FunctionName(), err)
						return fmt.Errorf("failed to set %s", PSInstanceSingular)
					}
				}
			} else if makingInstanceCount == 0 {
				makeOnDemandInstanceInput.MaxCount = aws.Int32(totalPendingInstance)
				//makeOnDemandInstanceInput.MaxCount = aws.Int32(totalAvailableInstance - totalFreeInstance)

				runInstanceOutput, err = MakeInstance(ctx, ec2Client, makeOnDemandInstanceInput)
				if err != nil {
					return fmt.Errorf("failed to create on-demand instance: %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
				}

				logrus.Infof("Making required free on-demand instances: %v", runInstanceOutput.Instances)
			}
		}
	}

	return err
}

func UpdateOccupiedInstance(ctx context.Context) (err error) {
	db, ok := ctx.Value("database").(*pgxpool.Pool)
	if !ok {
		return fmt.Errorf("unable to get database connection")
	}

	q := `UPDATE
	pixel_streaming_instance AS psi
SET
	status = 'occupied'
FROM
	pixel_streaming_sessions AS pss
WHERE
	psi.id = pss.instance_id
	AND psi.status = 'free'
	AND pss.status = 'running'`

	_, err = db.Exec(ctx, q)

	if err != nil {
		logrus.Errorf("failed to update instance status %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
		return fmt.Errorf("failed to update instance status %s", PSInstancePlural)
	}

	return nil
}

func TerminateClosedSessionsInstance(ctx context.Context) (err error) {
	db, ok := ctx.Value("database").(*pgxpool.Pool)
	if !ok {
		return fmt.Errorf("unable to get database connection")
	}

	var ec2Client = ec2.NewFromConfig(cfg)

	var regions map[uuid.UUID]string
	regions, err = GetRegions(ctx)

	for regionId, regionName := range regions {
		q := `SELECT
    psi.instance_id, psi.region_id
FROM
	pixel_streaming_sessions pss
	INNER JOIN pixel_streaming_instance psi ON psi.id = pss.instance_id AND psi.region_id = $1	
	AND pss.status = 'closed'`

		var rows pgx.Rows
		defer func() {
			rows.Close()
		}()

		rows, err = db.Query(ctx, q, regionId)
		if err != nil {
			logrus.Errorf("failed to update instance status %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
			return fmt.Errorf("failed to update instance status %s", PSInstancePlural)
		}

		var (
			terminateInstanceIds []string
		)

		var instance = &PixelStreamingInstance{}
		for rows.Next() {
			err = rows.Scan(&instance.InstanceId, &instance.RegionId)
			if err != nil {
				logrus.Errorf("failed to scan %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
				return fmt.Errorf("failed to get %s", PSInstancePlural)
			}

			terminateInstanceIds = append(terminateInstanceIds, *instance.InstanceId)
		}

		cfg, err = config.LoadDefaultConfig(
			ctx,
			config.WithRegion(regionName),
			config.WithClientLogMode(aws.LogRequestWithBody),
			config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(AwsAccessKey, AwsSecretKey, "")),
		)

		err = TerminateInstances(ctx, ec2Client, terminateInstanceIds)

		if err != nil {
			return fmt.Errorf("failed to terminate: %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
		} else {
			data := PixelStreamingInstanceMetadata{
				Status: aws.String("deleted"),
			}

			for _, id := range terminateInstanceIds {
				data.InstanceId = &id
				err = UpdatePixelStreamingInstance(ctx, nil, data)
				if err != nil {
					return fmt.Errorf("failed to update running instance data: %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
				}
			}
		}
	}

	return nil
}

func CountAWSInstancesByState(instances []types.Instance, states ...string) (count int32) {
	for _, instance := range instances {
		instanceState := string(instance.State.Name)
		if slices.Contains(states, instanceState) {
			count++
		}
	}

	return count
}

func StopInstances(ctx context.Context, api EC2API, instanceIds []string) (err error) {
	var (
		stopInstanceOutput *ec2.StopInstancesOutput
	)

	stopInstanceInput.InstanceIds = instanceIds
	stopInstanceOutput, err = StopInstance(ctx, api, stopInstanceInput)
	if err != nil {
		return fmt.Errorf("failed to stop instances: %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
	}

	logrus.Infof("stop instance for incoming users: %v", stopInstanceOutput.StoppingInstances)

	return nil
}

func TerminateInstances(ctx context.Context, api EC2API, instanceIds []string) (err error) {
	var (
		terminateInstanceOutput *ec2.TerminateInstancesOutput
	)

	terminateInstanceInput.InstanceIds = instanceIds
	terminateInstanceOutput, err = TerminateInstance(ctx, api, terminateInstanceInput)
	if err != nil {
		return fmt.Errorf("failed to terminate instances: %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
	}

	logrus.Infof("terminate instances: %v", terminateInstanceOutput.TerminatingInstances)

	return nil
}

func GetInstanceIds(ctx context.Context, regionId uuid.UUID, instanceType string, statuses ...string) (freeInstanceIds []string, pendingInstanceUUIDs []uuid.UUID, err error) {
	db, ok := ctx.Value("database").(*pgxpool.Pool)
	if !ok {
		return nil, nil, fmt.Errorf("unable to get database connection")
	}

	var rows pgx.Rows
	rows, err = db.Query(ctx, `SELECT id, instance_id, status FROM pixel_streaming_instance WHERE region_id = $1 AND instance_type = $2 AND status = ANY($3)`, regionId, instanceType, statuses)

	defer func() {
		rows.Close()
	}()

	var (
		r = &PixelStreamingInstance{}
	)

	for rows.Next() {
		err = rows.Scan(&r.Id, &r.InstanceId, &r.Status)
		if err != nil {
			logrus.Errorf("failed to scan %s @ %s: %v", PSInstancePlural, reflect.FunctionName(), err)
			return nil, nil, fmt.Errorf("failed to get %s", PSInstancePlural)
		}

		if *r.Status == "free" {
			freeInstanceIds = append(freeInstanceIds, *r.InstanceId)
		} else if *r.Status == "pending" {
			pendingInstanceUUIDs = append(pendingInstanceUUIDs, *r.Id)
		}
	}

	return freeInstanceIds, pendingInstanceUUIDs, nil
}

func NewEC2Filter(name string, values ...string) types.Filter {
	awsValues := []string{}
	for _, value := range values {
		awsValues = append(awsValues, value)
	}

	filter := types.Filter{
		Name:   aws.String(name),
		Values: awsValues,
	}

	return filter
}
