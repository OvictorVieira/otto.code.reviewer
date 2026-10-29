package github

import "testing"

func TestParseThreadsReadsReactionsRepliesAndResolution(t *testing.T) {
	raw := []byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
	 {"isResolved":true,"path":"a.go","line":9,"comments":{"nodes":[
	  {"databaseId":1,"body":"finding","author":{"login":"otto[bot]"},
	   "reactionGroups":[{"content":"THUMBS_UP","reactors":{"totalCount":0}},{"content":"THUMBS_DOWN","reactors":{"totalCount":1}}]},
	  {"databaseId":2,"body":"Won't fix. reason","author":{"login":"dev"},"reactionGroups":[]}]}}]}}}}}`)
	threads, err := parseThreads(raw)
	if err != nil || len(threads) != 1 {
		t.Fatalf("parse: %v %+v", err, threads)
	}
	th := threads[0]
	if !th.Resolved || th.Path != "a.go" || th.Line != 9 || len(th.Comments) != 2 {
		t.Fatalf("bad thread: %+v", th)
	}
	if th.Comments[0].Down != 1 || th.Comments[0].Up != 0 || th.Comments[1].Body != "Won't fix. reason" {
		t.Fatalf("bad comments: %+v", th.Comments)
	}
}
