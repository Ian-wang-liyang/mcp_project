# Module 12: MCP resources

## Primer: resources

A RESOURCE is a piece of data the server exposes under a URI: a file, a DB row, a config blob.
Tools are chosen by the MODEL; resources are usually chosen by the APP or the user (an "attach
file" menu), which then puts the contents into the model's context.

The client side, all on *mcp.ClientSession:

```
cs.ListResources(ctx, nil)            → the fixed resources:   {URI, Name, MIMEType, ...}
cs.ListResourceTemplates(ctx, nil)    → URI patterns:          {URITemplate: "notes://{id}", ...}
cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "notes://42"})
    → res.Contents []*mcp.ResourceContents, each {URI, MIMEType, Text OR Blob}
```

The server side:

```
s.AddResource(&mcp.Resource{URI: ..., Name: ..., MIMEType: ...}, handler)          one URI
s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: ..., Name: ...}, handler) a URI family
handler is an mcp.ResourceHandler:
  func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error)
req.Params.URI is the exact URI the client asked for.
```

The SDK only calls your handler for a URI that matches a registered resource exactly or matches
a template. Anything else gets a "Resource not found" error without reaching your code. If a
content block leaves URI or MIMEType empty, the SDK copies them from the registration.

**FROM PYTHON/JS:** FastMCP's @mcp.resource("notes://{id}") decorator registers a template and passes
`id` as a function argument. The Go SDK hands you the whole URI and you pull the id out yourself.

**READ NEXT:** docs/server.md#resources (including "Binary resources" and "Resource subscriptions")
in the SDK repo (github.com/modelcontextprotocol/go-sdk/tree/main/docs).

## Lesson 12.1: AddReadme

**CONCEPT:** a static text resource

```
s.AddResource(&mcp.Resource{
    URI:      "docs://readme",   // must parse as a URI with a scheme
    Name:     "readme",          // required by the protocol; a short identifier
    MIMEType: "text/markdown",   // tells the app how to show or parse the contents
}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
    return &mcp.ReadResourceResult{
        Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/markdown", Text: "..."}},
    }, nil
})
Contents is a slice because one read can return several parts (e.g. every file in a folder).
Registering a resource makes the server advertise the "resources" capability.
```

**TASK:** Register a resource on s with URI "docs://readme", Name "readme", MIMEType "text/markdown"
(plus a Description if you like). Reading it returns one text content block, ReadmeText.

**Worked example:**

```go
const ReadmeText = "# Notes server\n\nRead notes with notes://{id}.\n"

func AddReadme(s *mcp.Server) {
	s.AddResource(&mcp.Resource{
		URI:         "docs://readme",
		Name:        "readme",
		Description: "How to use this server",
		MIMEType:    "text/markdown",
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/markdown", Text: ReadmeText}},
		}, nil
	})
}
```

## Lesson 12.2: AddNotesTemplate

**CONCEPT:** resource templates (and "not found")

```
s.AddResourceTemplate(&mcp.ResourceTemplate{
    URITemplate: "notes://{id}",   // an RFC 6570 URI template
    Name:        "note",
    MIMEType:    "text/plain",
}, handler)
One handler serves the whole family: notes://1, notes://abc, ... A simple {id} matches one
path segment, so notes://a/b does NOT match (the SDK answers "not found" for you), but the
empty notes:// DOES, so your handler can see an empty id.
The handler gets the full URI; cut the prefix off to get the id:
  id, ok := strings.CutPrefix(req.Params.URI, "notes://")
When the id doesn't exist, return the SDK's not-found error so every client recognizes it:
  return nil, mcp.ResourceNotFoundError(req.Params.URI)
It's a *jsonrpc.Error with Code mcp.CodeResourceNotFound (-32602 since SDK v1.7.0) and the URI
in Data. A plain errors.New("not found") also reaches the client, but with code 0, which no
client can tell apart from any other failure.
ListResourceTemplates is how a client discovers the pattern; ListResources doesn't show it.
```

**TASK:** Register a template "notes://{id}" (Name "note", MIMEType "text/plain") on s. Reading
`notes://<id>` returns one text block with notes[id]. For an id not in notes (including an
empty one), return mcp.ResourceNotFoundError.

**Worked example:**

```go
func AddNotesTemplate(s *mcp.Server, notes map[string]string) {
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "notes://{id}",
		Name:        "note",
		Description: "A note, by id",
		MIMEType:    "text/plain",
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := req.Params.URI
		id, _ := strings.CutPrefix(uri, "notes://")
		text, ok := notes[id]
		if !ok {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "text/plain", Text: text}},
		}, nil
	})
}
```

## Lesson 12.3: AddLogo

**CONCEPT:** binary resources

```
ResourceContents carries EITHER Text (a string) OR Blob (a []byte):
  {URI: "images://logo.png", MIMEType: "image/png", Blob: pngBytes}
On the wire JSON can't hold raw bytes, so the SDK base64-encodes Blob when sending and decodes
it when receiving. You never call base64 yourself: the client's Contents[0].Blob is the exact
bytes you put in. A client tells text from binary by which field is set.
Don't put binary in Text: a Go string can hold any bytes, but JSON text must be valid UTF-8,
so bytes like 0x89 or 0xFF get replaced and the image is corrupted.
```

**FROM PYTHON/JS:** the low-level Python types carry base64 in a `blob` string; here the field is []byte and the
encoding is automatic (like Buffer → base64 done for you).

**TASK:** Register a resource on s with URI "images://logo.png", Name "logo", MIMEType "image/png".
Reading it returns one content block whose Blob is LogoPNG (and no Text).

**Worked example:**

```go
var LogoPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\xff")

func AddLogo(s *mcp.Server) {
	s.AddResource(&mcp.Resource{URI: "images://logo.png", Name: "logo", MIMEType: "image/png"},
		func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{
				Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "image/png", Blob: LogoPNG}},
			}, nil
		})
}
```

## Lesson 12.4: ReadNote

**CONCEPT:** recognizing "not found" on the client

```
ReadResource returns an error for a missing resource; it's a protocol error, so there's no
IsError-style result like tools have. The error wraps a *jsonrpc.Error
(package github.com/modelcontextprotocol/go-sdk/jsonrpc); unwrap it with errors.As:
  var wireErr *jsonrpc.Error
  if errors.As(err, &wireErr) && wireErr.Code == mcp.CodeResourceNotFound { ... }
That lets you treat "no such note" as a normal answer and every other error (connection gone,
handler failure) as a real failure.
Note: CodeResourceNotFound equals jsonrpc.CodeInvalidParams (-32602), the code the spec chose,
so another kind of invalid-params error would look the same. Check wireErr.Message or Data if
you need to be strict.
```

**FROM PYTHON/JS:** like catching McpError and checking error.code, but Go's errors.As walks the
wrapped chain instead of an except clause matching a class.

**TASK:** Read `notes://<id>` from cs and return its text with found = true. If the server says the
resource doesn't exist, return ("", false, nil). Return any other error as err.

**Worked example:**

```go
func ReadNote(ctx context.Context, cs *mcp.ClientSession, id string) (text string, found bool, err error) {
	res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "notes://" + id})
	if err != nil {
		var wireErr *jsonrpc.Error
		if errors.As(err, &wireErr) && wireErr.Code == mcp.CodeResourceNotFound {
			return "", false, nil
		}
		return "", false, err
	}
	if len(res.Contents) == 0 {
		return "", false, errors.New("note has no contents")
	}
	return res.Contents[0].Text, true, nil
}
```

## Lesson 12.5: NewConfigServer (bonus)

**CONCEPT:** resource subscriptions, server side

```
A client can subscribe to a resource URI and be told when it changes. The server opts in with
two hooks, which must be set TOGETHER (NewServer panics if only one is):
  mcp.NewServer(impl, &mcp.ServerOptions{
      SubscribeHandler:   func(ctx context.Context, req *mcp.SubscribeRequest) error { ... },
      UnsubscribeHandler: func(ctx context.Context, req *mcp.UnsubscribeRequest) error { ... },
  })
req.Params.URI is the URI. Return nil to accept; the SDK then remembers which session
subscribed to which URI. Setting the hooks also advertises capabilities.resources.subscribe.
Without them, a client's Subscribe is silently ignored on current protocol versions.
When the data changes, publish it:
  s.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: "config://app"})
That notifies only the sessions subscribed to that URI. The notification carries just the URI,
not the new contents; the client re-reads the resource if it cares.
Don't block in these hooks: they run while the server handles the request.
```

**TASK:** Return a server named "config" (Version "v1.0.0") with a text resource "config://app"
(Name "app", contents of your choice) that supports subscriptions. Its SubscribeHandler sends
`"subscribe <uri>"` on events and its UnsubscribeHandler sends `"unsubscribe <uri>"` (the test reads
events, so a plain send is fine; a select with default is safer).

**Worked example:**

```go
func NewConfigServer(events chan<- string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "config", Version: "v1.0.0"}, &mcp.ServerOptions{
		SubscribeHandler: func(_ context.Context, req *mcp.SubscribeRequest) error {
			select {
			case events <- "subscribe " + req.Params.URI:
			default:
			}
			return nil
		},
		UnsubscribeHandler: func(_ context.Context, req *mcp.UnsubscribeRequest) error {
			select {
			case events <- "unsubscribe " + req.Params.URI:
			default:
			}
			return nil
		},
	})
	s.AddResource(&mcp.Resource{URI: "config://app", Name: "app", MIMEType: "text/plain"},
		func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{
				Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "theme=dark\n"}},
			}, nil
		})
	return s
}
```

## Lesson 12.6: WatchOptions (bonus)

**CONCEPT:** resource subscriptions, client side

```
Updates arrive through a callback you set when creating the client:
  mcp.NewClient(impl, &mcp.ClientOptions{
      ResourceUpdatedHandler: func(ctx context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
          ... req.Params.URI changed ...
      },
  })
then cs.Subscribe(ctx, &mcp.SubscribeParams{URI: ...}) and later cs.Unsubscribe(...).
Notification handlers run on the SDK's goroutines: hand the work off (e.g. on a channel) and
never block, or you stall the connection. A non-blocking send:
  select { case ch <- v: default: }
Gotcha: on the current protocol, Subscribe returns BEFORE the server has registered the
subscription, and it returns nil even if the server doesn't support subscriptions. An update
published in that gap is simply missed.
```

**TASK:** Return client options whose ResourceUpdatedHandler sends the updated URI on updates without
blocking.

**Worked example:**

```go
func WatchOptions(updates chan<- string) *mcp.ClientOptions {
	return &mcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
			select {
			case updates <- req.Params.URI:
			default:
			}
		},
	}
}
```
