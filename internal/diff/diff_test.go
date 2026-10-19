package diff

import "testing"

const sample = `diff --git a/src/stock.go b/src/stock.go
index 1111111..2222222 100644
--- a/src/stock.go
+++ b/src/stock.go
@@ -10,4 +10,5 @@ func Reserve() {
 	ctx := context.Background()
-	qty := 1
+	qty := req.Qty
+	log.Println(qty)
 	return nil
 }
diff --git a/old.txt b/old.txt
deleted file mode 100644
--- a/old.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-gone
-away
diff --git a/new.md b/new.md
new file mode 100644
--- /dev/null
+++ b/new.md
@@ -0,0 +1,2 @@
+# Title
+body
`

func TestCommentableLines(t *testing.T) {
	d := Parse(sample)

	cases := []struct {
		path string
		line int
		want bool
	}{
		{"src/stock.go", 10, true},  // context line
		{"src/stock.go", 11, true},  // added
		{"src/stock.go", 12, true},  // added
		{"src/stock.go", 14, true},  // last context line of the hunk
		{"src/stock.go", 9, false},  // before the hunk
		{"src/stock.go", 15, false}, // after the hunk
		{"new.md", 1, true},
		{"new.md", 2, true},
		{"old.txt", 1, false}, // deleted file has no right side
		{"missing.go", 1, false},
	}
	for _, c := range cases {
		if got := d.Commentable(c.path, c.line); got != c.want {
			t.Errorf("Commentable(%q, %d) = %v, want %v", c.path, c.line, got, c.want)
		}
	}
}

func TestSameHunk(t *testing.T) {
	d := Parse(sample)
	if !d.SameHunk("src/stock.go", 10, 14) {
		t.Error("lines 10 and 14 are in the same hunk")
	}
	if d.SameHunk("src/stock.go", 10, 20) {
		t.Error("line 20 is outside the hunk")
	}
}

func TestFiles(t *testing.T) {
	d := Parse(sample)
	got := d.Files()
	want := []string{"src/stock.go", "new.md"}
	if len(got) != len(want) {
		t.Fatalf("Files() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Files() = %v, want %v", got, want)
		}
	}
}
