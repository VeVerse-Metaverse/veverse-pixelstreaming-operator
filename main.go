package main

import (
	"context"
	"time"
)

var makeInstanceInterval = 60 * time.Second
var pendingInstanceUInterval = 60 * time.Second

var (
	AVAILABILITY_CHECK_TIME = time.Duration(60) * time.Second
)

func main() {

	ctx := context.Background()

	//region Database
	var err error
	ctx, err = DatabaseOpen(ctx)
	if err != nil {
		Logger.Fatalf("failed to setup database: %v", err)
	}

	defer func(ctx context.Context) {
		err = DatabaseClose(ctx)
		if err != nil {
			Logger.Fatalf("failed to shutdown database: %v", err)
		}
	}(ctx)

	for {
		err = CheckAvailabilitySpotInstance(ctx)
		if err != nil {
			Logger.Errorf("failed to check spot availability instance: %v", err)
			return
		}

		err = CheckAvailabilityOnDemandInstance(ctx)
		if err != nil {
			Logger.Errorf("failed to check on-demand availability instance: %v", err)
			return
		}

		err = UpdateOccupiedInstance(ctx)
		if err != nil {
			Logger.Errorf("failed to update occupied instance: %v", err)
			return
		}

		err = TerminateClosedSessionsInstance(ctx)
		if err != nil {
			Logger.Errorf("failed to terminate closed session instance: %v", err)
			return
		}

		time.Sleep(AVAILABILITY_CHECK_TIME)
	}
}
