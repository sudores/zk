package markdown

import (
	"github.com/yuin/goldmark/ast"
)

// LinkPosition holds the source position of a link.
type LinkPosition struct {
	Start int // Byte offset of '['
	End   int // Byte offset after ')'
}

// GetLinkPosition finds the byte offsets for a Link node.
// Returns (start of '[', end after ')' or ']').
// Returns nil if position cannot be determined.
func GetLinkPosition(link *ast.Link, source []byte) *LinkPosition {
	// Use the upstream position for the start of the link.
	linkStart := link.Pos()
	if linkStart < 0 {
		return nil
	}

	// Find the position of the next sibling node (or EOF)…
	searchStart := len(source)
	if next := ast.Node(link).NextSibling(); next != nil {
		searchStart = next.Pos()
	}

	// … Then walk backwards to the first `]` or `)`.
	for i := searchStart - 1; i > linkStart; i-- {
		if source[i] == ')' || source[i] == ']' {
			return &LinkPosition{linkStart, i + 1}
		}
	}

	return nil
}
