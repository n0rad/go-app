package version

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGenerateDateCommitVersion(t *testing.T) {
	assert.Equal(t, generateDateCommitVersion(42, "68cdd17", time.Date(2006, 1, 2, 0, 0, 0, 0, time.UTC)), "42.060102.0-H68cdd17")
	assert.Equal(t, generateDateCommitVersion(42, "68cdd17", time.Date(2006, 1, 2, 3, 4, 5, 6, time.UTC)), "42.060102.304-H68cdd17")
}
