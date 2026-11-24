package media

import (
	_ "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
	_ "github.com/mycroblog/plugin/gen/my/plugin/probe/v1" // want `R4: internal/media imports only gen/my/plugin/media`
	_ "github.com/mycroblog/plugin/internal/common"        // want `R7: internal/common is neither a protocol kind nor listed`
	_ "github.com/mycroblog/plugin/internal/probe"         // want `R4: internal/media imports no other internal/ package but the shared ones`
	_ "github.com/mycroblog/plugin/internal/server"        // want `R4: internal/media imports no other internal/ package but the shared ones`
	_ "github.com/mycroblog/plugin/plugins/b"              // want `R1: the root module does not import plugins/`
	_ "github.com/mycroblog/plugin/tools/x"                // want `R6: nothing outside tools/ imports tools/`
)
