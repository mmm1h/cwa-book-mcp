package zlib

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// WithHTTPClient supplies the embedding service's bounded transport. Upstream
// defaults and its CLI are intentionally not used by cwa-book-mcp.
func WithHTTPClient(h *http.Client) ClientOption { return func(c *Client){c.httpClient=h;c.downloadRetries=0} }
// WithRequestContext applies cancellation to metadata calls lacking a context
// argument upstream. Each service operation constructs a separate Client.
func WithRequestContext(ctx context.Context) ClientOption {return func(c *Client){c.requestContext=ctx}}
func(c *Client)doHTTP(req *http.Request)(*http.Response,error){if c.requestContext!=nil{req=req.Clone(c.requestContext)};return c.httpClient.Do(req)}
// OpenDownloadStream keeps destination naming, size limits and retry decisions
// in the embedding service while reusing upstream EAPI URL resolution.
func(c *Client)OpenDownloadStream(ctx context.Context,h *http.Client,rawURL string)(*http.Response,error){
	if !c.loggedIn{return nil,ErrNotLoggedIn}
	if c.mode==ClientModeEAPI{v,e:=c.resolveEAPIFileURL(ctx,rawURL);if e!=nil{return nil,e};rawURL=v}
	req,e:=c.newDownloadRequest(ctx,rawURL);if e!=nil{return nil,e}
	base,_:=url.Parse(c.domain)
	if base==nil||base.Scheme!=req.URL.Scheme||!strings.EqualFold(base.Host,req.URL.Host){req.Header.Del("Cookie");req.Header.Del("Authorization");req.Header.Del("remix-userid");req.Header.Del("remix-userkey")}
	return h.Do(req)
}
