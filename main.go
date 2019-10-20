package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
)

type responseWriter struct {
	http.ResponseWriter
}

func (rw responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, _ := rw.ResponseWriter.(http.Hijacker)
	rwc, buf, err := hj.Hijack()
	twrc := &teeReaderCloser{Reader: io.TeeReader(rwc, os.Stdout), Writer: io.MultiWriter(rwc, os.Stdout), Source: rwc}
	return twrc, buf, err
}

type roundTripper struct {
	http.RoundTripper
}

func (rt roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := rt.RoundTripper.RoundTrip(req)
	backConn, ok := res.Body.(io.ReadWriteCloser)
	if !ok {
		panic("cant assert")
	}
	// res.Body = &teeReaderCloser{Reader: io.TeeReader(backConn, os.Stdout), Writer: io.MultiWriter(backConn, os.Stdout), Source: backConn}
	return res, err
}

type teeReaderCloser struct {
	net.Conn
	io.Reader
	io.Writer
	io.Closer
	Source io.ReadWriteCloser
}

func (reader *teeReaderCloser) Close() error {
	return reader.Source.Close()
}

func serveReverseProxy(rw http.ResponseWriter, req *http.Request) {
	req.URL.Host = "echo.websocket.org"
	req.Host = "echo.websocket.org"
	req.URL.Scheme = "https"
	req.Header.Set("Host", "echo.websocket.org")
	fmt.Println(req.Host)
	proxy := httputil.NewSingleHostReverseProxy(req.URL)
	rt := roundTripper{http.DefaultTransport}
	proxy.Transport = rt
	proxy.ServeHTTP(responseWriter{rw}, req)
}

func main() {
	http.HandleFunc("/", serveReverseProxy)
	if err := http.ListenAndServe("127.0.0.1:8080", nil); err != nil {
		panic(err)
	}
}
