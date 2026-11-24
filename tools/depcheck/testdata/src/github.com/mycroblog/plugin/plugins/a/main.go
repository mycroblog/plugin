package main

import (
	_ "github.com/mycroblog/plugin/gen/my/plugin/media/v1"
	_ "github.com/mycroblog/plugin/internal/media"
	_ "github.com/mycroblog/plugin/internal/probe"
	_ "github.com/mycroblog/plugin/internal/server"
	_ "github.com/mycroblog/plugin/plugins/a/lib"
	_ "github.com/mycroblog/plugin/plugins/b" // want `R2: a plugin does not import another plugin`
)
