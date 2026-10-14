# 合成测试文件

`synthetic.epub` 和 `synthetic.pdf` 是本项目生成的原创测试内容，无真实书籍或账号数据。
用 `go run ./tools/testbook -out testdata` 重建。EPUB 的 ZIP 时间戳固定为零值，内容固定。
大文件只在忽略目录生成：`go run ./tools/testbook -out .work/books -pdf-bytes 1073741824`。
