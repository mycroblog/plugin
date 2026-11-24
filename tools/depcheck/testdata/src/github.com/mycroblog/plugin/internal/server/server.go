package server

import (
	_ "github.com/mycroblog/plugin/gen/my/plugin/probe/v1" // want `R3: internal/server, the transport, imports no protocol package`
	_ "github.com/mycroblog/plugin/internal/probe"         // want `R3: internal/server imports no other internal/ package`
	_ "net/http"
)
