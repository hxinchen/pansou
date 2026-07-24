package api

import (
	"net/http/pprof"

	"github.com/gin-gonic/gin"
)

// registerAdminPprof keeps profiling endpoints behind the same administrator
// authentication middleware as the rest of /api/admin.
func registerAdminPprof(group *gin.RouterGroup) {
	group.GET("/debug/pprof/", gin.WrapF(pprof.Index))
	group.GET("/debug/pprof/cmdline", gin.WrapF(pprof.Cmdline))
	group.GET("/debug/pprof/profile", gin.WrapF(pprof.Profile))
	group.POST("/debug/pprof/symbol", gin.WrapF(pprof.Symbol))
	group.GET("/debug/pprof/symbol", gin.WrapF(pprof.Symbol))
	group.GET("/debug/pprof/trace", gin.WrapF(pprof.Trace))
	for _, name := range []string{"allocs", "block", "goroutine", "heap", "mutex", "threadcreate"} {
		group.GET("/debug/pprof/"+name, gin.WrapH(pprof.Handler(name)))
	}
}
