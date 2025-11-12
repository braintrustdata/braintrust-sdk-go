package temporal

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/sdk/workflow"
)

const (
	TaskQueue   = "main"
	ProjectName = "temporal-example"
)

// Activities contains the Temporal activities
type Activities struct{}

// Process processes a number
func (a *Activities) Process(ctx context.Context, number int) (string, error) {
	return fmt.Sprintf("processed number: %d", number), nil
}

// ProcessWorkflow is a simple workflow that processes a number
func ProcessWorkflow(ctx workflow.Context, number int) (string, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var activities *Activities
	var result string

	err := workflow.ExecuteActivity(ctx, activities.Process, number).Get(ctx, &result)
	if err != nil {
		return "", err
	}

	return result, nil
}
