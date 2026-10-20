package vcr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/dnaeon/go-vcr.v3/cassette"
)

func TestScrubBodiesRemovesIdentity(t *testing.T) {
	errorBody := `{"Code":"ForbiddenError","Message":"Missing read access [user_email=someone@example.org] [user_org=Acme Corp] [timestamp=1]"}`
	i := &cassette.Interaction{
		Request: cassette.Request{Body: `{"owner":"someone@example.org","name":"go-sdk-tests"}`},
		Response: cassette.Response{
			Body:          errorBody,
			ContentLength: int64(len(errorBody)),
		},
	}
	require.NoError(t, scrubBodies(i))

	assert.Equal(t, `{"owner":"user@example.com","name":"go-sdk-tests"}`, i.Request.Body)
	want := `{"Code":"ForbiddenError","Message":"Missing read access [user_email=user@example.com] [user_org=REDACTED] [timestamp=1]"}`
	assert.Equal(t, want, i.Response.Body)
	assert.Equal(t, int64(len(want)), i.Response.ContentLength)
}

func TestScrubBodiesKeepsUnknownLength(t *testing.T) {
	i := &cassette.Interaction{Response: cassette.Response{Body: `[user_org=Acme]`, ContentLength: -1}}
	require.NoError(t, scrubBodies(i))
	assert.Equal(t, `[user_org=REDACTED]`, i.Response.Body)
	assert.Equal(t, int64(-1), i.Response.ContentLength)
}
