package domain

import (
	"testing"
)

func TestMatchingIsConservativeAndUnicodeNormalized(t *testing.T) {
	want := Book{Title: "Café: 副标题", Authors: []string{"Test Author"}, Language: "zh-Hant"}
	good := Book{Title: "CAFE\u0301: 副标题", Authors: []string{" Test   Author "}, Language: "chi"}
	if !Match(want, good, false) {
		t.Fatal("NFC/case/space normalization failed")
	}
	for _, bad := range []Book{{Title: "Café", Authors: good.Authors, Language: "zh"}, {Title: good.Title, Language: "zh"}, {Title: good.Title, Authors: good.Authors, Language: "en"}} {
		if Match(want, bad, false) {
			t.Fatal("missing/conflicting identity was accepted", bad)
		}
	}
	if !Match(want, Book{Title: "Different metadata", Language: "zh"}, true) {
		t.Fatal("explicit resolution not accepted")
	}
	if Match(want, Book{Title: "Different", Language: "en"}, true) {
		t.Fatal("resolution bypassed language")
	}
	if Language("中文书名但没有语言") == "zh" {
		t.Fatal("guessed language")
	}
}
func TestFilenameRejectsPathsAndUnsupportedTypes(t *testing.T) {
	for _, name := range []string{"../book.pdf", "C:\\book.epub", "book.exe", "book\n.pdf", ".hidden.pdf"} {
		if e := Filename(name); e == nil {
			t.Fatal("bad name accepted", name)
		}
	}
	if e := Filename("中文测试书.epub"); e != nil {
		t.Fatal(e)
	}
}
