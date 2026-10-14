package zlib

import("bytes";"encoding/json";"fmt")
// Search responses cannot allocate an unbounded slice even inside a bounded
// HTTP body. The service's page size is always <=50.
type boundedBooks []eapiBook
func(b *boundedBooks)UnmarshalJSON(data []byte)error{d:=json.NewDecoder(bytes.NewReader(data));d.UseNumber();token,e:=d.Token();if e!=nil||token!=json.Delim('['){return fmt.Errorf("%w: invalid books array",ErrParseFailed)};out:=make([]eapiBook,0,50);for d.More(){if len(out)>=50{return fmt.Errorf("%w: metadata page exceeds 50 books",ErrParseFailed)};var item eapiBook;if e=d.Decode(&item);e!=nil{return e};out=append(out,item)};_,e=d.Token();*b=out;return e}
