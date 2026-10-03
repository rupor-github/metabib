package inpxutil

import "testing"

func TestCompilationCollectorDetectsParts(t *testing.T) {
	t.Parallel()

	collector := &CompilationCollector{books: []compilationBook{
		{
			folder: "owner.zip",
			file:   "owner.fb2",
			root: &compilationSection{key: "owner", children: []*compilationSection{
				{key: "part-a", leaf: true},
				{key: "part-b", leaf: true},
				{key: "part-a", leaf: true},
				{key: "missing", leaf: true},
			}},
		},
		{folder: "a.zip", file: "a.fb2", root: &compilationSection{key: "part-a", leaf: true}},
		{folder: "b.zip", file: "b.fb2", root: &compilationSection{key: "part-b", leaf: true}},
	}}

	outputs := collector.compilations()
	if len(outputs) != 1 {
		t.Fatalf("compilations = %#v, want one", outputs)
	}
	got := outputs[0]
	if got.Folder != "owner.zip" || got.File != "owner.fb2" || got.Covered {
		t.Fatalf("compilation owner = %#v", got)
	}
	if len(got.Compilation) != 3 || got.Compilation[0].Part != 0 || got.Compilation[0].File != "a.fb2" ||
		got.Compilation[1].Part != 1 || got.Compilation[1].File != "b.fb2" ||
		got.Compilation[2].Part != 2 || got.Compilation[2].File != "a.fb2" {
		t.Fatalf("parts = %#v", got.Compilation)
	}
}
