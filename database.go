package main

import (
	"context"
	"fmt"
	"github.com/gofrs/uuid"
	"github.com/jackc/pgtype"
	pgtypeuuid "github.com/jackc/pgtype/ext/gofrs-uuid"
	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/log/logrusadapter"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/sirupsen/logrus"
	"os"
	"veverse-pixelstreaming-operator/reflect"
)

func DatabaseOpen(ctx context.Context) (context.Context, error) {
	host := os.Getenv("DATABASE_HOST")
	port := os.Getenv("DATABASE_PORT")
	user := os.Getenv("DATABASE_USER")
	pass := os.Getenv("DATABASE_PASS")
	name := os.Getenv("DATABASE_NAME")

	url := fmt.Sprintf("postgres://%s:%s@%s:%s/%s", user, pass, host, port, name)

	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		Logger.Fatal(err)
	}

	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		conn.ConnInfo().RegisterDataType(pgtype.DataType{
			Value: &pgtypeuuid.UUID{},
			Name:  "uuid",
			OID:   pgtype.UUIDOID,
		})
		return nil
	}

	logger := &logrus.Logger{
		Out:          os.Stderr,
		Formatter:    new(logrus.JSONFormatter),
		Hooks:        make(logrus.LevelHooks),
		Level:        logrus.InfoLevel,
		ExitFunc:     os.Exit,
		ReportCaller: false,
	}

	env := os.Getenv("ENVIRONMENT")
	if env != "prod" {
		config.ConnConfig.Logger = logrusadapter.NewLogger(logger)
	}

	pool, err := pgxpool.ConnectConfig(ctx, config)
	if err != nil {
		return ctx, fmt.Errorf("unable to connect to database: %v", err)
	}

	ctx = context.WithValue(ctx, "database", pool)

	return ctx, nil
}

func DatabaseClose(ctx context.Context) error {
	db, ok := ctx.Value("database").(*pgxpool.Pool)
	if !ok {
		return fmt.Errorf("unable to get database connection")
	}

	db.Close()

	return nil
}

func GetReleases(ctx context.Context) ([]Release, error) {
	var releases []Release

	db, ok := ctx.Value("database").(*pgxpool.Pool)
	if !ok {
		return releases, fmt.Errorf("unable to get database connection")
	}

	rows, err := db.Query(ctx, "SELECT id, app_id, name, version FROM releases")
	if err != nil {
		return releases, err
	}

	for rows.Next() {
		var release Release
		err := rows.Scan(
			&release.Id,
			&release.AppId,
			&release.Name,
			&release.Version)
		if err != nil {
			return releases, err
		}

		releases = append(releases, release)
	}

	return releases, nil
}

func GetRegions(ctx context.Context) (regions map[uuid.UUID]string, err error) {
	db, ok := ctx.Value("database").(*pgxpool.Pool)
	if !ok {
		return regions, fmt.Errorf("unable to get database connection")
	}

	q := `SELECT id, name FROM region`

	var rows pgx.Rows
	rows, err = db.Query(ctx, q)

	if err != nil {
		logrus.Errorf("failed to query %s @ %s: %v", RegionPlural, reflect.FunctionName(), err)
		return nil, fmt.Errorf("failed to get %s", RegionPlural)
	}

	regions = make(map[uuid.UUID]string)
	for rows.Next() {
		var region Region
		err = rows.Scan(&region.Id, &region.Name)

		regions[*region.Id] = region.Name
	}

	return regions, nil
}

func IndexPixelStreamingInstances(ctx context.Context, status string, regionId uuid.UUID) (instances []PixelStreamingInstance, err error) {
	db, ok := ctx.Value("database").(*pgxpool.Pool)
	if !ok {
		return instances, fmt.Errorf("unable to get database connection")
	}

	q := `SELECT id, region_id, host, port, status, instance_id FROM pixel_streaming_instance WHERE status = $1 AND region_id = $2`

	var rows pgx.Rows
	rows, err = db.Query(ctx, q, status, regionId)

	if err != nil {
		logrus.Errorf("failed to query %s @ %s: %v", RegionPlural, reflect.FunctionName(), err)
		return nil, fmt.Errorf("failed to get %s", RegionPlural)
	}

	for rows.Next() {
		var instance PixelStreamingInstance
		err = rows.Scan(
			&instance.Id,
			&instance.ReleaseId,
			&instance.RegionId,
			&instance.Host,
			&instance.Port,
			&instance.Status,
			&instance.InstanceId,
		)

		if err != nil {
			return instances, err
		}

		instances = append(instances, instance)
	}

	return instances, nil
}

func UpdatePixelStreamingInstance(ctx context.Context, id *uuid.UUID, data PixelStreamingInstanceMetadata) (err error) {
	logrus.Infof("change instance data to: %v instanceID: %s", data, id)

	q := `UPDATE pixel_streaming_instance SET`
	isUpdate := false
	if data.InstanceId != nil {
		isUpdate = true
		q += fmt.Sprintf(" instance_id = '%s'", *data.InstanceId)
	}

	if data.Host != nil {
		if isUpdate {
			q += ","
		} else {
			isUpdate = true
		}

		q += fmt.Sprintf(" host = '%s'", *data.Host)
	}

	if data.Port != nil {
		if isUpdate {
			q += ","
		} else {
			isUpdate = true
		}

		q += fmt.Sprintf(" port = %d", *data.Port)
	}

	if &data.Status != nil {
		if isUpdate {
			q += ","
		} else {
			isUpdate = true
		}

		q += fmt.Sprintf(" status = '%s'", *data.Status)
	}

	if isUpdate {
		db, ok := ctx.Value("database").(*pgxpool.Pool)
		if !ok {
			return fmt.Errorf("unable to get database connection")
		}

		q += ` WHERE id = $1 OR instance_id = $2`

		_, err = db.Exec(ctx, q, id, data.InstanceId)
		if err != nil {
			logrus.Errorf("failed to update instance %s @ %s: %v", PSInstanceSingular, reflect.FunctionName(), err)
			return fmt.Errorf("failed to update instance %s", PSInstanceSingular)
		}
	}

	return nil
}
