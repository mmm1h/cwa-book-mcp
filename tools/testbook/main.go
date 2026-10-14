// testbook generates small original books; it is not part of the service image.
package main

import("flag";"fmt";"os";"path/filepath";"github.com/mmm1h/cwa-book-mcp/internal/testfixture")
func main(){out:=flag.String("out","testdata","output directory");size:=flag.Int64("pdf-bytes",1024,"PDF size in bytes");flag.Parse();if e:=os.MkdirAll(*out,0700);e!=nil{panic(e)};for _,format:=range []string{"epub","pdf"}{path:=filepath.Join(*out,"synthetic."+format);f,e:=os.Create(path);if e!=nil{panic(e)};if format=="epub"{e=testfixture.EPUB(f)}else{e=testfixture.PDF(f,*size)};if e!=nil{panic(e)};if e=f.Close();e!=nil{panic(e)};fmt.Println(path)}}
