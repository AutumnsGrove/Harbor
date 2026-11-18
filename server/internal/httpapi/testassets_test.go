package httpapi

import "testing/fstest"

// testAssets stands in for web.Assets(): the real embedded build is exercised by
// the packaging check and the demo, not by handler tests.
func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html":            {Data: []byte("<!doctype html><html><body>Harbor shell</body></html>")},
		"_app/immutable/app.js": {Data: []byte("console.log('harbor')")},
	}
}

// emptyAssets is a binary built without a frontend (`go build`, not the image).
func emptyAssets() fstest.MapFS { return fstest.MapFS{} }
