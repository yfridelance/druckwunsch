package web

import (
	"encoding/json"
	"net/url"
	"strings"
)

// bookmarkletJS runs on the model page in the user's browser. It collects
// OpenGraph data and, on MakerWorld, the design object with all print
// profiles: first from __NEXT_DATA__, otherwise via the site's own API
// (same-origin, so Cloudflare and login work as usual). It then navigates to
// Druckwunsch, carrying the data in the URL fragment – navigation is not
// restricted by the source site's CSP, unlike fetch or form posts.
const bookmarkletJS = `(async()=>{
const B=__BASE__;
const g=p=>{const e=document.querySelector('meta[property="'+p+'"],meta[name="'+p+'"]');return e?e.content:''};
const find=(v,d)=>{if(!v||typeof v!=='object'||d>14)return null;if(!Array.isArray(v)&&Array.isArray(v.instances)&&(v.title||v.titleTranslated))return v;for(const k in v){const r=find(v[k],d+1);if(r)return r}return null};
let design=null;
const mw=/(^|\.)makerworld\.com(\.cn)?$/.test(location.hostname);
if(mw){
try{const n=document.getElementById('__NEXT_DATA__');design=find(n?JSON.parse(n.textContent):window.__NEXT_DATA__,0)}catch(e){}
const m=location.pathname.match(/models\/(\d+)/);
if(!design&&m){try{const r=await fetch('/api/v1/design-service/design/'+m[1],{credentials:'include',headers:{accept:'application/json'}});if(r.ok)design=find(await r.json(),0)}catch(e){}}
if(design){for(const k of ['comments','commentList','relatedDesigns','recommendDesigns'])delete design[k]}
}
const p={url:location.href,og:{title:g('og:title')||document.title,description:g('og:description')||g('description'),image:g('og:image'),siteName:g('og:site_name'),author:g('author')},design:design};
location.href=B+'/ingest#'+encodeURIComponent(JSON.stringify(p));
})();void 0`

func bookmarklet(origin string) string {
	base, _ := json.Marshal(origin)
	js := strings.ReplaceAll(bookmarkletJS, "__BASE__", string(base))
	lines := strings.Split(js, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return "javascript:" + url.PathEscape(strings.Join(lines, ""))
}
