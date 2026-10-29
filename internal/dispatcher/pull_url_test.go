package dispatcher

import "testing"

func TestParsePullURLReadsOnlyAGitHubPullRequestPage(t *testing.T) {
	repo, number, err := ParsePullURL("https://github.com/octo/scratch/pull/7")
	if err != nil || repo != "octo/scratch" || number != 7 {
		t.Fatalf("got %q, %d, %v", repo, number, err)
	}
	for _, raw := range []string{"", "http://github.com/octo/scratch/pull/7", "https://evil.example/octo/scratch/pull/7",
		"https://github.com/octo/scratch/issues/7", "https://github.com/octo/scratch/pull/7/files", "https://github.com/octo/scratch/pull/x",
		"https://github.com/octo/scratch/pull/0", "https://github.com/octo/scratch/pull/-3",
		"https://github.com/../scratch/pull/7", "https://github.com/octo/../pull/7", "https://github.com/./scratch/pull/7", "https://github.com/oc%20to/scratch/pull/7"} {
		if _, _, err := ParsePullURL(raw); err == nil {
			t.Errorf("ParsePullURL(%q) read a PR out of it", raw)
		}
	}
}
