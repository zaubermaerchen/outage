package outage_test

// This file guards the release workflow's Homebrew caller and tag eligibility.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func releaseWorkflow(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

func TestHomebrewReleaseWorkflowContract(t *testing.T) {
	workflow := releaseWorkflow(t)
	job := regexp.MustCompile(`(?ms)^  homebrew:\n(.*?)(?:^  [a-z][a-z_-]*:\n|\z)`).FindStringSubmatch(workflow)
	if job == nil {
		t.Fatal("release workflow must call the common Homebrew workflow")
	}
	for _, required := range []string{
		"    if: github.event_name == 'push' && needs.publish.outputs.homebrew-eligible == 'true'\n",
		"    needs: publish\n",
		"    permissions:\n      contents: read\n",
		"      formula: outage\n",
		"      tag: ${{ github.ref_name }}\n",
		"      app-id: ${{ vars.HOMEBREW_TAP_APP_ID }}\n",
		"    secrets:\n      app-private-key: ${{ secrets.HOMEBREW_TAP_APP_PRIVATE_KEY }}\n",
	} {
		if !strings.Contains(job[1], required) {
			t.Errorf("Homebrew caller missing contract: %q", required)
		}
	}
	pin := regexp.MustCompile(`(?m)^    uses: zaubermaerchen/homebrew-tap/\.github/workflows/update-formula\.yml@([a-f0-9]{40})$`).FindStringSubmatch(job[1])
	if pin == nil {
		t.Fatal("Homebrew caller must pin the tap's common workflow")
	}
	if !strings.Contains(job[1], "      automation-ref: "+pin[1]+"\n") {
		t.Error("common workflow and automation checkout must use the same revision")
	}
	for _, forbidden := range []string{"always()", "contents: write", "pull-requests: write", "    steps:", "    runs-on:", "secrets: inherit"} {
		if strings.Contains(job[1], forbidden) {
			t.Errorf("Homebrew caller must not include %q", forbidden)
		}
	}
}

func TestReleasePublishHomebrewEligibility(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release workflow runs Bash on Ubuntu")
	}
	workflow := releaseWorkflow(t)
	if !strings.Contains(workflow, "      homebrew-eligible: ${{ steps.homebrew-eligibility.outputs.homebrew-eligible }}\n") {
		t.Error("publish must expose Homebrew eligibility to its dependent job")
	}
	script := regexp.MustCompile(`(?ms)^      - name: Classify Homebrew release\n        id: homebrew-eligibility\n        shell: bash\n        env:\n          VERSION: \$\{\{ github.ref_name \}\}\n        run: \|\n(.*?)(?:\n  homebrew:|\z)`).FindStringSubmatch(workflow)
	if script == nil {
		t.Fatal("Homebrew eligibility step not found")
	}
	for _, tc := range []struct{ tag, eligible string }{
		{"v1.2.3", "true"},
		{"v0.0.0", "true"},
		{"v1.2.3-alpha", "false"},
		{"v1.2.3+build", "false"},
		{"v01.2.3", "false"},
		{"v1.02.3", "false"},
		{"v1.2.03", "false"},
		{"v1.2", "false"},
		{"v1.2.3.4", "false"},
		{"v1.2.x", "false"},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), "output")
			command := exec.Command("bash", "-c", script[1])
			command.Env = append(os.Environ(), "VERSION="+tc.tag, "GITHUB_OUTPUT="+outputPath)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("eligibility script failed: %v\n%s", err, output)
			}
			output, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			want := "homebrew-eligible=" + tc.eligible + "\n"
			if string(output) != want {
				t.Fatalf("eligibility output = %q, want %q", output, want)
			}
		})
	}
}
