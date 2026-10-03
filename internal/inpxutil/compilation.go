package inpxutil

import (
	"archive/zip"
	jsonv2 "encoding/json/v2"
	"fmt"
	"os"

	"go.uber.org/zap"

	"metabib/model"
)

type CompilationCollector struct {
	path    string
	meta    Metadata
	log     *zap.Logger
	books   []compilationBook
	missing int
}

type compilationBook struct {
	folder string
	file   string
	root   *compilationSection
}

type compilationSection struct {
	key      string
	leaf     bool
	children []*compilationSection
}

type compilationOutput struct {
	Folder      string                  `json:"folder"`
	File        string                  `json:"file"`
	Compilation []compilationOutputPart `json:"compilation"`
	Covered     bool                    `json:"covered"`
}

type compilationOutputPart struct {
	Part   int    `json:"part"`
	Folder string `json:"folder"`
	File   string `json:"file"`
}

func NewCompilationCollector(path string, meta Metadata, log *zap.Logger) *CompilationCollector {
	return &CompilationCollector{path: path, meta: meta, log: log}
}

func (c *CompilationCollector) AddRecord(rec model.DatasetRecord, folder string, file string) {
	fingerprint := recordFB2BodyFingerprint(rec)
	if fingerprint == nil || len(fingerprint.Sections) == 0 {
		c.missing++
		return
	}
	root := compilationSectionTree(fingerprint.Sections)
	if root == nil {
		c.missing++
		return
	}
	c.books = append(c.books, compilationBook{folder: folder, file: file, root: root})
}

func (c *CompilationCollector) Write() error {
	if c.missing > 0 && c.log != nil {
		c.log.Warn("Some INPX records lack FB2 body fingerprints", zap.Int("records", c.missing))
	}
	outputs := c.compilations()
	if len(outputs) == 0 {
		if c.log != nil {
			c.log.Warn("Skipping INPX compilations output because no compilations were detected")
		}
		if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove empty INPX compilations output %q: %w", c.path, err)
		}
		return nil
	}
	data, err := jsonv2.Marshal(outputs)
	if err != nil {
		return fmt.Errorf("marshal INPX compilations JSON: %w", err)
	}
	f, err := os.Create(c.path)
	if err != nil {
		return fmt.Errorf("create INPX compilations output %q: %w", c.path, err)
	}
	zw := zip.NewWriter(f)
	zw.SetComment(ZipComment(c.meta))
	if err := WriteZipText(zw, "compilations.json", string(data)); err != nil {
		_ = zw.Close()
		_ = f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		return fmt.Errorf("close INPX compilations zip %q: %w", c.path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close INPX compilations output %q: %w", c.path, err)
	}
	return nil
}

func (c *CompilationCollector) compilations() []compilationOutput {
	rootToBooks := make(map[string][]int, len(c.books))
	for idx, book := range c.books {
		rootToBooks[book.root.key] = append(rootToBooks[book.root.key], idx)
	}
	outputs := make([]compilationOutput, 0)
	for idx, book := range c.books {
		parts, covered, found := c.compilationParts(idx, rootToBooks)
		if found <= 1 {
			continue
		}
		outputs = append(outputs, compilationOutput{Folder: book.folder, File: book.file, Compilation: parts, Covered: covered})
	}
	return outputs
}

func (c *CompilationCollector) compilationParts(owner int, rootToBooks map[string][]int) ([]compilationOutputPart, bool, int) {
	book := c.books[owner]
	found := make(map[string]struct{})
	notFound := make(map[string]struct{})
	var parts []compilationOutputPart
	var walk func([]*compilationSection)
	walk = func(sections []*compilationSection) {
		for _, section := range sections {
			if section.key == book.root.key {
				walk(section.children)
				continue
			}
			matches := rootToBooks[section.key]
			if len(matches) > 0 {
				part := len(found)
				for _, match := range matches {
					matched := c.books[match]
					parts = append(parts, compilationOutputPart{Part: part, Folder: matched.folder, File: matched.file})
				}
				found[section.key] = struct{}{}
				continue
			}
			if section.leaf {
				notFound[section.key] = struct{}{}
				continue
			}
			walk(section.children)
		}
	}
	walk(book.root.children)
	return parts, len(notFound) == 0, len(found)
}

func recordFB2BodyFingerprint(rec model.DatasetRecord) *model.FB2BodyFingerprint {
	for _, artifact := range rec.Artifacts {
		if artifact.Fingerprints != nil && artifact.Fingerprints.FB2Body != nil {
			return artifact.Fingerprints.FB2Body
		}
	}
	return nil
}

func compilationSectionTree(sections []model.FB2BodySectionFingerprint) *compilationSection {
	var root *compilationSection
	stack := make([]*compilationSection, 0)
	for _, section := range sections {
		node := &compilationSection{key: section.Key, leaf: section.Leaf}
		if section.Depth == 0 {
			if root != nil {
				return nil
			}
			root = node
			stack = []*compilationSection{node}
			continue
		}
		if root == nil || section.Depth > len(stack) {
			return nil
		}
		stack = stack[:section.Depth]
		parent := stack[len(stack)-1]
		parent.children = append(parent.children, node)
		stack = append(stack, node)
	}
	return root
}
