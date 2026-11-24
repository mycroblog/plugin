package depcheck_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/mycroblog/plugin/tools/depcheck"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), depcheck.Analyzer, "github.com/mycroblog/plugin/...")
}
