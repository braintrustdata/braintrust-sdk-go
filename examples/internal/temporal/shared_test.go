package temporal

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.temporal.io/sdk/testsuite"
)

// TestProcess tests the simple number processing activity
func TestProcess(t *testing.T) {
	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestActivityEnvironment()

	activities := &Activities{}
	env.RegisterActivity(activities.Process)

	val, err := env.ExecuteActivity(activities.Process, 42)
	assert.NoError(t, err)

	var result string
	err = val.Get(&result)
	assert.NoError(t, err)
	assert.Contains(t, result, "42")
}

// TestProcessWorkflow tests the simple workflow
func TestProcessWorkflow(t *testing.T) {
	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestWorkflowEnvironment()

	activities := &Activities{}
	env.RegisterActivity(activities.Process)

	// Execute workflow with a number
	env.ExecuteWorkflow(ProcessWorkflow, 42)

	assert.True(t, env.IsWorkflowCompleted())
	assert.NoError(t, env.GetWorkflowError())

	var result string
	err := env.GetWorkflowResult(&result)
	assert.NoError(t, err)
	assert.Contains(t, result, "42")
}
