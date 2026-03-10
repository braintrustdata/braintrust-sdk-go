package trace

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeDecodeV4_RoundTrip(t *testing.T) {
	c := SpanComponents{
		ObjectType: SpanObjectTypeProjectLogs,
		ObjectID:   "my-project",
		RowID:      "1234567890abcdef",
		SpanID:     "1234567890abcdef",
		RootSpanID: "a1b2c3d4e5f6789012345678abcdef01",
	}

	encoded, err := EncodeV4(c)
	require.NoError(t, err)
	assert.NotEmpty(t, encoded)

	decoded, err := DecodeV4(encoded)
	require.NoError(t, err)
	assert.Equal(t, c.ObjectType, decoded.ObjectType)
	assert.Equal(t, c.ObjectID, decoded.ObjectID)
	assert.Equal(t, c.RowID, decoded.RowID)
	assert.Equal(t, c.SpanID, decoded.SpanID)
	assert.Equal(t, c.RootSpanID, decoded.RootSpanID)
}

func TestDecodeV4_MinimalPayload(t *testing.T) {
	c := SpanComponents{ObjectType: SpanObjectTypeProjectLogs, ObjectID: "p1"}
	encoded, err := EncodeV4(c)
	require.NoError(t, err)
	decoded, err := DecodeV4(encoded)
	require.NoError(t, err)
	assert.Equal(t, "p1", decoded.ObjectID)
	assert.Equal(t, SpanObjectTypeProjectLogs, decoded.ObjectType)
}

func TestEncodeDecodeV4_WithSpanIds(t *testing.T) {
	c := SpanComponents{
		ObjectType: SpanObjectTypeExperiment,
		ObjectID:   "exp-uuid-here",
		RowID:      "1234567890abcdef",
		SpanID:     "1234567890abcdef",
		RootSpanID: "a1b2c3d4e5f6789012345678abcdef01",
	}
	encoded, err := EncodeV4(c)
	require.NoError(t, err)
	decoded, err := DecodeV4(encoded)
	require.NoError(t, err)
	assert.Equal(t, c.SpanID, decoded.SpanID)
	assert.Equal(t, c.RootSpanID, decoded.RootSpanID)
	assert.Equal(t, SpanObjectTypeExperiment, decoded.ObjectType)
}

func TestParentFromComponents_ProjectName(t *testing.T) {
	c := SpanComponents{ObjectType: SpanObjectTypeProjectLogs, ObjectID: "my-project"}
	p, err := ParentFromComponents(c)
	require.NoError(t, err)
	assert.Equal(t, ParentTypeProjectName, p.Type)
	assert.Equal(t, "my-project", p.ID)
}

func TestParentFromComponents_ProjectID(t *testing.T) {
	c := SpanComponents{ObjectType: SpanObjectTypeProjectLogs, ObjectID: "550e8400-e29b-41d4-a716-446655440000"}
	p, err := ParentFromComponents(c)
	require.NoError(t, err)
	assert.Equal(t, ParentTypeProjectID, p.Type)
	assert.Equal(t, "550e8400-e29b-41d4-a716-446655440000", p.ID)
}

func TestParentFromComponents_Experiment(t *testing.T) {
	c := SpanComponents{ObjectType: SpanObjectTypeExperiment, ObjectID: "exp-123"}
	p, err := ParentFromComponents(c)
	require.NoError(t, err)
	assert.Equal(t, ParentTypeExperimentID, p.Type)
	assert.Equal(t, "exp-123", p.ID)
}
