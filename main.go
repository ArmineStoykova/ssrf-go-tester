package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type TestInput struct {
	Endpoint   string `json:"endpoint"`
	Method     string `json:"method"`
	HeaderText string `json:"headers"`
	Body       string `json:"body"`
	TestURL    string `json:"test_url"`
}
type Result struct {
	Status     int    `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Response   string `json:"response"`
	Error      string `json:"error,omitempty"`
	Note       string `json:"note"`
}
type Event struct {
	Time   string `json:"time"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Remote string `json:"remote"`
}

var mu sync.Mutex
var events []Event

// Local process controller; credentials never enter the browser or log output.
type oobController struct {
	sync.Mutex
	server, client             *exec.Cmd
	serverCancel, clientCancel context.CancelFunc
	token, lastError           string
	serverUp, clientUp         bool
}

var oob oobController
var tokenPattern = regexp.MustCompile(`Client Token:\s*([a-fA-F0-9]{32,128})`)

func (c *oobController) status() map[string]any {
	c.Lock()
	defer c.Unlock()
	return map[string]any{"server": c.serverUp, "client": c.clientUp, "error": c.lastError}
}
func (c *oobController) start() error {
	c.Lock()
	if c.serverUp || c.clientUp {
		c.Unlock()
		return fmt.Errorf("Interactsh already running from this app; stop it first")
	}
	c.lastError = ""
	c.token = ""
	c.Unlock()
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	serverBin := filepath.Join(home, "go/bin/interactsh-server")
	clientBin := filepath.Join(home, "go/bin/interactsh-client")
	for _, name := range []string{serverBin, clientBin} {
		if _, err := os.Stat(name); err != nil {
			return fmt.Errorf("missing binary: %s", name)
		}
	}
	// Explicitly bind all services to loopback; no public listener.
	args := []string{"-domain", "oob.test", "-listen-ip", "127.0.0.1", "-dns-port", "8053", "-http-port", "8081", "-https-port", "8443", "-smtp-port", "2525", "-smtps-port", "2587", "-smtp-autotls-port", "2465", "-ldap-port", "10389", "-skip-acme", "-auth", "-verbose"}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, serverBin, args...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}
	c.Lock()
	c.server = cmd
	c.serverCancel = cancel
	c.serverUp = true
	c.Unlock()
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(pipe)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		signaled := false
		for scanner.Scan() {
			line := scanner.Text()
			if match := tokenPattern.FindStringSubmatch(line); len(match) > 1 && !signaled {
				signaled = true
				ready <- match[1]
			}
			if strings.Contains(line, "[FTL]") || strings.Contains(line, "[ERR]") {
				c.Lock()
				c.lastError = "Interactsh server reported an error; check for occupied ports"
				c.Unlock()
			}
		}
		if !signaled {
			ready <- ""
		}
	}()
	go func() {
		err := cmd.Wait()
		c.Lock()
		c.serverUp = false
		c.server = nil
		c.token = ""
		if err != nil && ctx.Err() == nil {
			c.lastError = "Interactsh server exited: " + err.Error()
		}
		c.Unlock()
	}()
	var token string
	select {
	case token = <-ready:
	case <-time.After(8 * time.Second):
	}
	if token == "" {
		cancel()
		return fmt.Errorf("server did not start; check whether ports 8081/8053/10389 are occupied (stop manually started server/client first)")
	}
	c.Lock()
	c.token = token
	c.Unlock()
	cctx, ccancel := context.WithCancel(context.Background())
	client := exec.CommandContext(cctx, clientBin, "-server", "127.0.0.1:8081", "-token", token, "-auth=false", "-json", "-o", "interactsh-events.jsonl", "-ps", "-psf", "interactsh-payload.txt")
	// Avoid writing token into logs, output, or browser. Capture only sanitized errors.
	out, err := client.StdoutPipe()
	if err != nil {
		ccancel()
		cancel()
		return err
	}
	client.Stderr = client.Stdout
	if err := client.Start(); err != nil {
		ccancel()
		cancel()
		return err
	}
	c.Lock()
	c.client = client
	c.clientCancel = ccancel
	c.clientUp = true
	c.Unlock()
	go func() {
		scanner := bufio.NewScanner(out)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "[FTL]") {
				c.Lock()
				c.lastError = "Interactsh client failed to register; check server status"
				c.Unlock()
			}
		}
	}()
	go func() {
		err := client.Wait()
		c.Lock()
		c.clientUp = false
		c.client = nil
		if err != nil && cctx.Err() == nil {
			c.lastError = "Interactsh client exited: " + err.Error()
		}
		c.Unlock()
	}()
	return nil
}
func (c *oobController) stop() {
	c.Lock()
	cc := c.clientCancel
	sc := c.serverCancel
	c.clientCancel = nil
	c.serverCancel = nil
	c.token = ""
	c.Unlock()
	if cc != nil {
		cc()
	}
	if sc != nil {
		sc()
	}
}
func controlHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		jsonOut(w, oob.status())
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	switch r.URL.Query().Get("action") {
	case "start":
		if err := oob.start(); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
	case "stop":
		oob.stop()
	default:
		http.Error(w, "invalid action", 400)
		return
	}
	jsonOut(w, oob.status())
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, htmlPage)
	})
	mux.HandleFunc("/api/test", testHandler)
	mux.HandleFunc("/api/interactsh/control", controlHandler)
	mux.HandleFunc("/api/interactsh", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		f, err := os.Open(filepath.Join(".", "interactsh-events.jsonl"))
		if os.IsNotExist(err) {
			jsonOut(w, []json.RawMessage{})
			return
		}
		if err != nil {
			http.Error(w, "cannot read OOB log", 500)
			return
		}
		defer f.Close()
		scanner := bufio.NewScanner(io.LimitReader(f, 4*1024*1024))
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		entries := make([]json.RawMessage, 0)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if json.Valid(line) {
				entries = append(entries, append(json.RawMessage(nil), line...))
			}
			if len(entries) > 100 {
				entries = entries[1:]
			}
		}
		if err := scanner.Err(); err != nil {
			http.Error(w, "cannot parse OOB log", 500)
			return
		}
		jsonOut(w, entries)
	})

	mux.HandleFunc("/api/interactsh/payload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		b, err := os.ReadFile("interactsh-payload.txt")
		if err != nil {
			jsonOut(w, map[string]string{"error": "No payload file. Restart interactsh-client with -ps -psf and keep it running."})
			return
		}
		lines := strings.Fields(string(b))
		if len(lines) == 0 {
			jsonOut(w, map[string]string{"error": "Payload file is empty."})
			return
		}
		payload := strings.TrimSpace(lines[len(lines)-1])
		if !strings.HasSuffix(payload, ".127.0.0.1:8081") && !strings.HasSuffix(payload, ".oob.test") {
			jsonOut(w, map[string]string{"error": "Unrecognized payload format. Check client configuration."})
			return
		}
		jsonOut(w, map[string]string{"payload": payload})
	})

	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}
		mu.Lock()
		out := append([]Event{}, events...)
		mu.Unlock()
		jsonOut(w, out)
	})
	mux.HandleFunc("/api/clear", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}
		mu.Lock()
		events = nil
		mu.Unlock()
		jsonOut(w, map[string]bool{"ok": true})
	})
	// A local-only callback receiver. A callback from a remote server requires an explicitly configured reachable receiver.
	mux.HandleFunc("/callback/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		events = append([]Event{{Time: time.Now().Format(time.RFC3339), Method: r.Method, Path: r.URL.RequestURI(), Remote: r.RemoteAddr}}, events...)
		if len(events) > 100 {
			events = events[:100]
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "SSRF test callback received")
	})
	addr := "127.0.0.1:8766"
	log.Printf("SSRF Go Tester: http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, securityHeaders(mux)))
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'")
		if r.Method == "POST" && r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://127.0.0.1:8766" {
			http.Error(w, "origin rejected", 403)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func testHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var in TestInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid input", 400)
		return
	}
	in.Method = strings.ToUpper(strings.TrimSpace(in.Method))
	if in.Method != "GET" && in.Method != "POST" && in.Method != "PUT" && in.Method != "PATCH" {
		http.Error(w, "unsupported method", 400)
		return
	}
	target, err := url.Parse(in.Endpoint)
	if err != nil || target.Scheme != "https" || target.Hostname() == "" || target.User != nil {
		http.Error(w, "target must be an HTTPS URL without credentials", 400)
		return
	}
	if strings.TrimSpace(in.TestURL) == "" {
		http.Error(w, "test URL required", 400)
		return
	}
	testURL, err := url.Parse(in.TestURL)
	if err != nil || (testURL.Scheme != "http" && testURL.Scheme != "https") || testURL.Hostname() == "" {
		http.Error(w, "test URL must be HTTP(S)", 400)
		return
	}
	// Only an explicitly selected endpoint receives the request. Test URL is inserted only via {{TEST_URL}} placeholders.
	body := strings.ReplaceAll(in.Body, "{{TEST_URL}}", in.TestURL)
	endpoint := strings.ReplaceAll(in.Endpoint, "{{TEST_URL}}", url.QueryEscape(in.TestURL))
	parsed, _ := url.Parse(endpoint)
	if parsed.Host != target.Host || parsed.Scheme != target.Scheme {
		http.Error(w, "endpoint host changed", 400)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), in.Method, endpoint, bytes.NewBufferString(body))
	if err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	for _, line := range strings.Split(in.HeaderText, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			http.Error(w, "headers must be Key: Value", 400)
			return
		}
		k = strings.TrimSpace(k)
		if strings.EqualFold(k, "Host") || strings.EqualFold(k, "Cookie") || strings.EqualFold(k, "Proxy-Authorization") {
			http.Error(w, "Host, Cookie and Proxy-Authorization headers are disabled", 400)
			return
		}
		req.Header.Set(k, strings.TrimSpace(v))
	}
	// Disallow loopback, private, link-local, multicast and other non-public destinations, including after DNS resolution.
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("no IPs")
		}
		for _, ip := range ips {
			if !ip.IP.IsGlobalUnicast() || ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() {
				return nil, errors.New("target resolves to a non-public address")
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	client := &http.Client{Timeout: 12 * time.Second, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	start := time.Now()
	resp, err := client.Do(req)
	result := Result{DurationMS: time.Since(start).Milliseconds(), Note: "A response or callback alone does not prove an exploitable SSRF. Verify authorization and server-side behavior."}
	if err != nil {
		result.Error = err.Error()
		jsonOut(w, result)
		return
	}
	defer resp.Body.Close()
	result.Status = resp.StatusCode
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	result.Response = string(b)
	jsonOut(w, result)
}

const htmlPage = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>SSRF Go Tester</title><style>body{font:15px system-ui;background:#101827;color:#e8edf6;margin:0;padding:30px}main{max-width:900px;margin:auto}h1{margin-bottom:5px}p{color:#a9b8cc}.panel{background:#1b283b;border:1px solid #35445b;border-radius:14px;padding:20px;margin:18px 0}label{display:block;margin:12px 0 5px;font-weight:600}input,select,textarea{box-sizing:border-box;width:100%;background:#0d1624;color:#f0f4fa;border:1px solid #4b5c72;border-radius:7px;padding:11px;font:14px monospace}textarea{min-height:85px}button{background:#f97316;color:#fff;border:0;border-radius:8px;padding:11px 17px;font-weight:700;cursor:pointer;margin-top:14px}button.secondary{background:#ea580c;color:#fff}button:hover{background:#fb923c}button.secondary:hover{background:#f97316}pre{white-space:pre-wrap;word-break:break-word;background:#0d1624;padding:14px;border-radius:8px;max-height:400px;overflow:auto}.hint{font-size:13px;color:#a9b8cc}.two{display:grid;grid-template-columns:1fr 1fr;gap:15px}@media(max-width:650px){.two{grid-template-columns:1fr}} .oob-table{width:100%;border-collapse:collapse;margin-top:14px;font-size:13px}.oob-table th,.oob-table td{padding:10px;border-bottom:1px solid #35445b;text-align:left;vertical-align:top;word-break:break-word}.oob-table th{color:#a9b8cc}.oob-scroll{overflow-x:auto}.oob-details{white-space:pre-wrap;overflow-wrap:anywhere;max-height:200px;overflow:auto}.oob-url{margin-top:10px}.notice{font-size:13px;color:#fbbf24}</style></head><body><main><h1>🛡 SSRF Go Tester</h1><p>Local Security QA workbench · Ubuntu · Go</p><div class="panel"><strong>Authorized testing only.</strong><p>Requests are sent only to your explicitly selected HTTPS endpoint. Use {{TEST_URL}} in the request body or query string. No internal-network scanning.</p><label>Target HTTPS endpoint</label><input id="endpoint" placeholder="https://stage.example.com/api/fetch"><div class="two"><div><label>HTTP method</label><select id="method"><option>POST</option><option>GET</option><option>PUT</option><option>PATCH</option></select></div><div><label>Test URL / callback URL</label><input id="test_url" placeholder="https://your-interactsh-url.example/test"></div></div><label>Headers (one per line)</label><textarea id="headers" placeholder="Content-Type: application/json&#10;Authorization: Bearer YOUR_TOKEN"></textarea><label>Request body</label><textarea id="body">{"url":"{{TEST_URL}}"}</textarea><button id="send">Send single test</button><p class="hint">No automatic payload generation. Avoid including credentials in exported results.</p></div><div class="panel"><h2>Response analysis</h2><pre id="result">No test sent yet.</pre><button class="secondary" id="export">Export result as JSON</button></div><div class="panel"><h2>Local callback log</h2><p class="hint">Receiver: http://127.0.0.1:8766/callback/anything (accessible only from this computer). For remote OOB tests use a separately reachable callback service such as Interactsh.</p><button class="secondary" id="refresh">Refresh callbacks</button> <button class="secondary" id="clear">Clear callbacks</button><pre id="events">[]</pre></div><div class="panel"><h2>Interactsh OOB Monitor</h2><div class="panel"><strong>Interactsh Control</strong><p id="oob_status">Checking status...</p><button id="oob_start">Start Interactsh</button> <button id="oob_stop" class="secondary">Stop Interactsh</button><p class="hint">Controls only processes started by this app. Stop manually launched Interactsh Server and Client first. Local-only testing.</p></div><p class="hint">Displays locally recorded Interactsh events (HTTP, DNS and other protocols). The Interactsh client must stay running. Remote callbacks require a reachable server and DNS configuration.</p><button class="secondary" id="oob_generate">Generate OOB URL</button> <button class="secondary" id="oob_copy">Copy URL</button> <button class="secondary" id="oob_fill">Use in test</button><input class="oob-url" id="oob_url" readonly placeholder="Generate from active Interactsh session"><p class="notice" id="oob_notice">Local Interactsh addresses (127.0.0.1 / oob.test) are not reachable by remote Stage servers. A real reachable domain and DNS configuration are required for remote OOB testing.</p><button class="secondary" id="oob_refresh">Refresh OOB events</button><div id="oob_events">No OOB interactions yet.</div></div></main><script>let last=null;const el=id=>document.getElementById(id);el('send').onclick=async()=>{el('result').textContent='Testing...';try{const r=await fetch('/api/test',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(Object.fromEntries(['endpoint','method','test_url','headers','body'].map(x=>[x,el(x).value])))});const txt=await r.text();try{last=JSON.parse(txt);el('result').textContent=JSON.stringify(last,null,2)}catch{el('result').textContent='HTTP '+r.status+'\n'+txt}}catch(e){el('result').textContent=String(e)}};el('refresh').onclick=async()=>{const r=await fetch('/api/events');el('events').textContent=JSON.stringify(await r.json(),null,2)};el('clear').onclick=async()=>{await fetch('/api/clear',{method:'POST'});el('refresh').click()};async function controlStatus(){try{const r=await fetch('/api/interactsh/control',{cache:'no-store'});const d=await r.json();el('oob_status').textContent='Server: '+(d.server?'Running':'Stopped')+' | Client: '+(d.client?'Running':'Stopped')+(d.error?' | '+d.error:'')}catch(e){el('oob_status').textContent='Status unavailable: '+e.message}}async function controlAction(action){el('oob_status').textContent=action==='start'?'Starting Interactsh...':'Stopping Interactsh...';try{const r=await fetch('/api/interactsh/control?action='+action,{method:'POST'});if(!r.ok)throw Error(await r.text());await controlStatus()}catch(e){el('oob_status').textContent='Error: '+e.message}}el('oob_start').onclick=()=>controlAction('start');el('oob_stop').onclick=()=>controlAction('stop');controlStatus();setInterval(controlStatus,5000);let oobUrl='';el('oob_generate').onclick=async()=>{try{const r=await fetch('/api/interactsh/payload',{cache:'no-store'});if(!r.ok)throw Error('HTTP '+r.status);const d=await r.json();if(d.error)throw Error(d.error);const suffix='probe-'+Math.random().toString(36).slice(2,12);oobUrl='http://'+d.payload+'/'+suffix;el('oob_url').value=oobUrl;el('oob_notice').textContent=d.payload.includes('127.0.0.1')||d.payload.endsWith('.oob.test')?'Local-only payload: not directly reachable/resolvable by a remote target. This is a test-specific URL derived from the current Interactsh session, not a new session.':'Test-specific URL from the active Interactsh session.'}catch(e){el('oob_notice').textContent=e.message}};el('oob_copy').onclick=async()=>{if(!oobUrl)return alert('Generate an OOB URL first');try{await navigator.clipboard.writeText(oobUrl)}catch(e){el('oob_url').select();document.execCommand('copy')}};el('oob_fill').onclick=()=>{if(!oobUrl)return alert('Generate an OOB URL first');el('test_url').value=oobUrl;window.scrollTo({top:0,behavior:'smooth'})};el('oob_refresh').onclick=async()=>{const root=el('oob_events');try{const r=await fetch('/api/interactsh',{cache:'no-store'});if(!r.ok)throw Error('HTTP '+r.status);const data=await r.json();root.replaceChildren();if(!data.length){root.textContent='No OOB interactions yet.';return}const wrap=document.createElement('div');wrap.className='oob-scroll';const table=document.createElement('table');table.className='oob-table';const head=document.createElement('tr');['Time','Protocol','Remote IP','ID','Details'].forEach(x=>{const th=document.createElement('th');th.textContent=x;head.append(th)});table.append(head);data.slice().reverse().forEach(e=>{const tr=document.createElement('tr');[e['timestamp']||'',e['protocol']||'',e['remote-address']||'',e['unique-id']||''].forEach(v=>{const td=document.createElement('td');td.textContent=String(v);tr.append(td)});const td=document.createElement('td');const details=document.createElement('details');const summary=document.createElement('summary');summary.textContent='View request / response';const pre=document.createElement('pre');pre.className='oob-details';pre.textContent=JSON.stringify(e,null,2);details.append(summary,pre);td.append(details);tr.append(td);table.append(tr)});wrap.append(table);root.append(wrap)}catch(e){root.textContent='Could not load OOB events: '+e.message}};el('oob_refresh').click();setInterval(()=>el('oob_refresh').click(),5000);el('export').onclick=()=>{if(!last)return alert('Run a test first');const a=document.createElement('a');a.href=URL.createObjectURL(new Blob([JSON.stringify(last,null,2)],{type:'application/json'}));a.download='ssrf-result.json';a.click();URL.revokeObjectURL(a.href)};</script></body></html>`
