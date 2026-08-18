package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVerdictNeedsFix_Policies(t *testing.T) {
	mk := func(sevs ...string) Verdict {
		var v Verdict
		for _, s := range sevs {
			v.Findings = append(v.Findings, struct {
				ID           string `json:"id"`
				Severity     string `json:"severity"`
				File         string `json:"file"`
				Observed     string `json:"observed"`
				Expected     string `json:"expected"`
				RequiredFix  string `json:"required_fix"`
				ForbiddenFix string `json:"forbidden_fix"`
			}{Severity: s})
		}
		return v
	}

	// только minor/advisory
	assert.False(t, mk("minor", "advisory").NeedsFix("blocking"))
	assert.False(t, mk("minor", "advisory").NeedsFix("major"))
	assert.False(t, mk("minor", "advisory").NeedsFix("")) // дефолт major
	assert.True(t, mk("minor").NeedsFix("all"))

	// major
	assert.False(t, mk("major").NeedsFix("blocking"))
	assert.True(t, mk("major").NeedsFix("major"))
	assert.True(t, mk("major").NeedsFix("all"))

	// blocking
	assert.True(t, mk("blocking").NeedsFix("blocking"))
	assert.True(t, mk("blocking").NeedsFix("major"))
	assert.True(t, mk("blocking").NeedsFix("all"))

	// пустой findings — ничего не чиним ни при какой политике
	assert.False(t, mk().NeedsFix("all"))
}
