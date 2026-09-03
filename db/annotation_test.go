package db

import "testing"

func TestAnnotationTextCleansBBCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "html tags are flattened first",
			body: `<p>Before <strong>bold</strong></p><p>after</p>`,
			want: "Before bold after",
		},
		{
			name: "formatting tags keep text",
			body: `Before [i]italic[/i] [color=red]red[/color] after`,
			want: "Before italic red after",
		},
		{
			name: "collapse keeps content",
			body: `[collapse collapsed title=Содержание] По щучьему велению [/collapse]`,
			want: "По щучьему велению",
		},
		{
			name: "url drops link text and image drops content",
			body: `Before [url=https://example.test]link text[/url] middle [img]https://example.test/pic.jpg[/img] after`,
			want: "Before link text middle after",
		},
		{
			name: "url drops enclosed link target",
			body: `Before [url]https://example.test/path[/url] after`,
			want: "Before after",
		},
		{
			name: "unknown bracket text stays",
			body: `Before [the old title] [a] good review after`,
			want: "Before [the old title] [a] good review after",
		},
		{
			name: "a link keeps text",
			body: `Before [a http://example.test]link text[/a] after`,
			want: "Before link text after",
		},
		{
			name: "unclosed media tag drops following url token",
			body: `Before [img]https://example.test/pic.jpg after`,
			want: "Before after",
		},
		{
			name: "punctuation placeholder drops body",
			body: `---`,
			want: "",
		},
		{
			name: "named placeholder drops body",
			body: `--skip--`,
			want: "",
		},
		{
			name: "single word body stays",
			body: `Рассказ`,
			want: "Рассказ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := AnnotationText(tt.body)
			if err != nil {
				t.Fatalf("AnnotationText() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("AnnotationText() = %q, want %q", got, tt.want)
			}
		})
	}
}
