package library

import (
	"strings"
	"testing"
)

func TestCheckXMLDepth(t *testing.T) {
	ok := strings.Repeat("<a>", 10) + strings.Repeat("</a>", 10)
	if err := checkXMLDepth(strings.NewReader(ok), 16); err != nil {
		t.Errorf("shallow XML rejected: %v", err)
	}
	deep := strings.Repeat("<a>", 300) + strings.Repeat("</a>", 300)
	if err := checkXMLDepth(strings.NewReader(deep), 256); err == nil {
		t.Error("deep XML accepted")
	}
}
