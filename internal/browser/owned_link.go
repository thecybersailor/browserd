package browser

import (
	"context"
	"github.com/chromedp/chromedp"
	"strconv"
)

// Follow selected links in the session's owned tab without changing click handlers.
func prepareOwnedLink(ctx context.Context, selector string) error {
	script := `(()=>{const element=document.querySelector(` + strconv.Quote(selector) + `);const link=element?.closest('a[href]');if(link instanceof HTMLAnchorElement)link.target='_self';})()`
	return chromedp.Run(ctx, chromedp.Evaluate(script, nil))
}
