package diff

import "testing"

func TestStatsCountsLinesNewAndDeletedFiles(t *testing.T) {
	raw := `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,2 +1,3 @@
 package a
-var x = 1
+var x = 2
+var y = 3
diff --git a/new.go b/new.go
new file mode 100644
--- /dev/null
+++ b/new.go
@@ -0,0 +1,2 @@
+package a
+func N() {}
diff --git a/old.go b/old.go
deleted file mode 100644
--- a/old.go
+++ /dev/null
@@ -1 +0,0 @@
-package a
`
	got := Stats(raw)
	want := []FileStat{
		{Path: "a.go", Added: 2, Removed: 1},
		{Path: "new.go", Added: 2, New: true},
		{Path: "old.go", Removed: 1, Deleted: true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("file %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
