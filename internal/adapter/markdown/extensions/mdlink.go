package extensions

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// MarkdownLinkExt is parses markdown links and tracks their positions.
//
// ast.Link does not track position, and the LSP needs link positions.
var MarkdownLinkExt = &markdownLinkExt{}

type markdownLinkExt struct{}

// MarkdownLink represents a markdown link with position tracking.
type MarkdownLink struct {
	ast.Link
	Destination []byte
	StartOffset int // Position of '['
	EndOffset   int // Position after ')'
}

// KindMarkdownLink is the kind of MarkdownLink nodes.
var KindMarkdownLink = ast.NewNodeKind("MarkdownLink")

func (e *markdownLinkExt) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(
		parser.WithInlineParsers(
			// Higher priority than default link parser (100)
			util.Prioritized(&mdLinkParser{}, 99),
		),
	)
}

type mdLinkParser struct{}

func (p *mdLinkParser) Trigger() []byte {
	return []byte{'['}
}

// Parse is called when a Trigger is found.
func (p *mdLinkParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, segment := block.PeekLine()

	// Skip images.
	if segment.Start > 0 {
		src := block.Source()
		if segment.Start > 0 && src[segment.Start-1] == '!' {
			return nil
		}
	}

	// Find the closing ].
	i := 1
	depth := 1
	for i < len(line) && depth > 0 {
		switch line[i] {
		case '\\':
			i++ // Escaped char.
		case '[':
			depth++
		case ']':
			depth--
		}
		i++
	}
	// i now points after ].

	if depth != 0 {
		return nil // Unclosed [.
	}

	if i >= len(line) || line[i] != '(' {
		return nil // Not an inline link
	}

	linkTextEnd := i - 1 // Position of ].
	i++                  // Skip (.

	// Parse destination until ')' with balanced parentheses.
	destStart := i
	parenDepth := 0
	for i < len(line) {
		c := line[i]
		if c == '\\' && i+1 < len(line) {
			i += 2
			continue
		}
		if c == '(' {
			parenDepth++
		} else if c == ')' {
			if parenDepth == 0 {
				break
			}
			parenDepth--
		}
		i++
	}

	if i >= len(line) || line[i] != ')' {
		return nil
	}

	destination := line[destStart:i]
	i++ // Skip ).

	link := &MarkdownLink{
		Destination: destination,
		StartOffset: segment.Start,
		EndOffset:   segment.Start + i,
	}

	linkText := line[1:linkTextEnd]
	if len(linkText) > 0 {
		textNode := ast.NewTextSegment(text.NewSegment(segment.Start+1, segment.Start+1+len(linkText)))
		link.AppendChild(link, textNode)
	}

	block.Advance(i)
	return link
}
