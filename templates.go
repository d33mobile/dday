package main

import "html/template"

// Shared CSS for the server-rendered pages, matching the landing page's dark
// theme. Kept inline so the pages need no external assets (CSP default-src
// 'self' with style-src 'unsafe-inline').
const pageCSS = `
:root{--bg:#0a0e14;--bg2:#0d1420;--fg:#e6edf3;--muted:#8b98a5;
  --accent:#39d353;--accent2:#2dd4bf;--card:rgba(255,255,255,.04);--border:rgba(255,255,255,.09)}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:'Courier New',ui-monospace,SFMono-Regular,Menlo,monospace;
  background:radial-gradient(1100px 700px at 50% -15%,#12203a 0%,var(--bg) 60%);
  color:var(--fg);min-height:100vh;line-height:1.5;-webkit-font-smoothing:antialiased;overflow-x:hidden}
.wrap{max-width:560px;margin:0 auto;padding:clamp(1.5rem,5vw,3rem) 1.25rem 2.5rem}
.card{background:var(--card);border:1px solid var(--border);border-radius:16px;padding:clamp(1.35rem,4vw,2.1rem)}
h1{font-size:clamp(1.6rem,5vw,2.2rem);font-weight:900;letter-spacing:-.03em;line-height:1.1;margin-bottom:.35rem;
  background:linear-gradient(120deg,var(--accent),var(--accent2));
  -webkit-background-clip:text;background-clip:text;-webkit-text-fill-color:transparent}
.sub{font-size:.75rem;letter-spacing:.14em;text-transform:uppercase;color:var(--muted);margin-bottom:1.5rem}
p{margin-bottom:.8rem;font-size:.95rem}
.big{font-size:clamp(2.4rem,12vw,3.6rem);font-weight:900;line-height:1;color:var(--accent);
  font-variant-numeric:tabular-nums;margin:.6rem 0 1rem;text-align:center}
label{display:block;font-size:.7rem;letter-spacing:.16em;text-transform:uppercase;color:var(--muted);margin:1rem 0 .35rem}
input{width:100%;font-family:inherit;font-size:1rem;color:var(--fg);background:var(--bg2);
  border:1px solid var(--border);border-radius:10px;padding:.75rem .85rem}
input:focus{outline:none;border-color:var(--accent2)}
input[readonly]{color:var(--muted);cursor:not-allowed}
.btn{display:inline-block;width:100%;font-family:inherit;font-size:1rem;font-weight:700;letter-spacing:.04em;
  margin-top:1.4rem;padding:.85rem 2rem;border-radius:11px;border:1px solid transparent;cursor:pointer;text-align:center;
  text-decoration:none;background:linear-gradient(120deg,var(--accent),var(--accent2));color:#04140a}
.btn:hover{filter:brightness(1.05)}
.btn-danger{background:none;border-color:rgba(255,80,80,.45);color:#ff8a8a}
.btn-danger:hover{background:rgba(255,80,80,.1);filter:none}
.status{text-align:center;font-size:.95rem;color:var(--muted);margin-bottom:.4rem}
.status b{color:var(--fg)}
.err{color:#ff8a8a;font-size:.9rem;background:rgba(255,80,80,.08);border:1px solid rgba(255,80,80,.25);
  border-radius:10px;padding:.7rem .85rem;margin-bottom:.6rem}
.wrap-wide{max-width:1080px}
.tablewrap{overflow-x:auto;margin-top:1.2rem;border:1px solid var(--border);border-radius:12px}
table{border-collapse:collapse;width:100%;font-size:.82rem}
th,td{padding:.55rem .7rem;text-align:left;white-space:nowrap;border-bottom:1px solid var(--border)}
th{font-size:.66rem;letter-spacing:.12em;text-transform:uppercase;color:var(--muted);
  background:rgba(255,255,255,.03);position:sticky;top:0}
tbody tr:last-child td{border-bottom:none}
tbody tr:hover{background:rgba(255,255,255,.03)}
td.num{font-variant-numeric:tabular-nums;color:var(--muted)}
.tag{display:inline-block;font-size:.68rem;letter-spacing:.06em;text-transform:uppercase;
  padding:.18rem .5rem;border-radius:999px;border:1px solid var(--border)}
.tag-ok{color:var(--accent);border-color:rgba(57,211,83,.35);background:rgba(57,211,83,.1)}
.tag-wait{color:#ffcf70;border-color:rgba(255,180,60,.35);background:rgba(255,180,60,.1)}
.sumgrid{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:.7rem;margin-top:.4rem}
.sumcell{background:var(--bg2);border:1px solid var(--border);border-radius:12px;padding:.75rem .85rem}
.sumcell .k{font-size:.66rem;letter-spacing:.12em;text-transform:uppercase;color:var(--muted)}
.sumcell .v{font-size:1.5rem;font-weight:900;font-variant-numeric:tabular-nums;margin-top:.15rem}
.foot{margin-top:1.6rem;font-size:.78rem;color:var(--muted)}
.foot a{color:var(--accent2);text-decoration:none}
.foot a:hover{text-decoration:underline}
`

// tmpl parses the named templates once at startup.
var tmpl = template.Must(template.New("dday").Parse(templatesSrc))

const templatesSrc = `
{{define "head"}}<!DOCTYPE html><html lang="pl"><head><meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>{{.Title}} · D-Day · Hakierspejs Łódź</title><style>` + pageCSS + `</style></head><body><main class="wrap"><div class="card">
<h1>D-Day</h1><div class="sub">Zapisy · Unconference · Hakierspejs Łódź</div>{{end}}

{{define "foot"}}<div class="foot"><a href="/privacy">Polityka prywatności / RODO</a></div>
</div></main></body></html>{{end}}

{{define "form"}}{{template "head" .}}
{{if .Error}}<div class="err">{{.Error}}</div>{{end}}
{{if .Waitlist}}<div class="err" style="color:#ffcf70;background:rgba(255,180,60,.08);border-color:rgba(255,180,60,.28)">Miejsca podstawowe są już zajęte. Ten zapis trafi na <b>listę rezerwową</b> — damy znać, jeśli zwolni się miejsce.</div>{{end}}
<p>Cześć <b>{{.Nick}}</b>! Uzupełnij dane, aby dokończyć zapis na D-Day.</p>
<form method="POST" action="/register">
<input type="hidden" name="t" value="{{.Token}}">
<label>Nick (z Matrix)</label>
<input type="text" value="{{.Nick}}" readonly>
<label for="city">Miejscowość</label>
<input id="city" name="city" type="text" value="{{.City}}" required maxlength="120" placeholder="np. Łódź">
<label for="email">E-mail</label>
<input id="email" name="email" type="email" value="{{.Email}}" required maxlength="200" placeholder="ty@example.com">
<button class="btn" type="submit">Zapisz się</button>
</form>
<p class="foot" style="margin-top:1.2rem">Wysyłając formularz wyrażasz zgodę na przetwarzanie danych zgodnie z <a href="/privacy">polityką prywatności</a>. Miejsc: {{.Count}}/{{.Limit}}.</p>
</div></main></body></html>{{end}}

{{define "success"}}{{template "head" .}}
<p>Zapisano! Twój numer uczestnika:</p>
<div class="big">#{{.Number}}</div>
<p>Do zobaczenia na D-Day, <b>{{.Nick}}</b>. Zapraszamy na wydarzenie — w razie potrzeby skontaktujemy się z Tobą przez czat Matrix.</p>
{{template "foot" .}}{{end}}

{{define "waitlist"}}{{template "head" .}}
<p>Miejsca podstawowe są już zajęte, ale zapisaliśmy Cię na <b>listę rezerwową</b>. Twoja pozycja:</p>
<div class="big">#{{.WaitlistPos}}</div>
<p>Damy znać przez czat Matrix, <b>{{.Nick}}</b>, jeśli zwolni się miejsce.</p>
{{template "foot" .}}{{end}}

{{define "duplicate"}}{{template "head" .}}
{{if .WaitlistPos}}<p>Jesteś już zapisany 🎉 na liście rezerwowej, pozycja:</p>
<div class="big">#{{.WaitlistPos}}</div>{{else}}<p>Jesteś już zapisany 🎉 Twój numer uczestnika:</p>
<div class="big">#{{.Number}}</div>{{end}}
<p>Nie musisz robić nic więcej, <b>{{.Nick}}</b>.</p>
{{template "foot" .}}{{end}}

{{define "panel"}}{{template "head" .}}
<p>Cześć <b>{{.Nick}}</b>! To Twój panel uczestnika D-Day.</p>
<p class="status">Twój numer uczestnika:</p>
<div class="big">#{{.Number}}</div>
{{if .WaitlistPos}}<p class="status">Status: <b>lista rezerwowa</b>, pozycja #{{.WaitlistPos}}. Damy znać przez czat Matrix, jeśli zwolni się miejsce.</p>
{{else}}<p class="status">Status: <b>uczestnik</b> — masz potwierdzone miejsce.</p>{{end}}
<form method="POST" action="/panel">
<input type="hidden" name="t" value="{{.Token}}">
<button class="btn btn-danger" type="submit">Wycofaj udział</button>
</form>
<p class="foot" style="margin-top:1.2rem">Wycofanie udziału usuwa Twoje zgłoszenie i zwalnia miejsce dla osoby z listy rezerwowej.</p>
{{template "foot" .}}{{end}}

{{define "panel_done"}}{{template "head" .}}
<p style="font-size:1.05rem">Udział wycofany.</p>
<p>Twoje zgłoszenie zostało usunięte, <b>{{.Nick}}</b>. Miejsce wraca do puli — jeśli ktoś czeka na liście rezerwowej, awansuje.</p>
<p>Jeśli zmienisz zdanie, możesz zapisać się ponownie — napisz <b>!start</b> do bota na Matrixie (o ile będą jeszcze wolne miejsca).</p>
{{template "foot" .}}{{end}}

{{define "admin"}}<!DOCTYPE html><html lang="pl"><head><meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="robots" content="noindex, nofollow">
<title>{{.Title}} · D-Day · Hakierspejs Łódź</title><style>` + pageCSS + `</style></head>
<body><main class="wrap wrap-wide"><div class="card">
<h1>D-Day</h1><div class="sub">Panel admina · podgląd zgłoszeń</div>
<div class="sumgrid">
<div class="sumcell"><div class="k">Uczestnicy</div><div class="v">{{.Confirmed}}/{{.SeatLimit}}</div></div>
<div class="sumcell"><div class="k">Lista rezerwowa</div><div class="v">{{.Waitlist}}/{{.WaitlistLimit}}</div></div>
<div class="sumcell"><div class="k">Łącznie</div><div class="v">{{.Total}}/{{.Capacity}}</div></div>
</div>
{{if .Rows}}<div class="tablewrap"><table>
<thead><tr><th>#</th><th>Status</th><th>Nick</th><th>Handle (MXID)</th><th>Miejscowość</th><th>E-mail</th><th>Data zapisu</th></tr></thead>
<tbody>
{{range .Rows}}<tr>
<td class="num">{{.Number}}</td>
<td>{{if .Confirmed}}<span class="tag tag-ok">{{.Status}}</span>{{else}}<span class="tag tag-wait">{{.Status}}</span>{{end}}</td>
<td><b>{{.Nick}}</b></td>
<td>{{.Handle}}</td>
<td>{{.City}}</td>
<td>{{.Email}}</td>
<td class="num">{{.Created}}</td>
</tr>
{{end}}</tbody></table></div>
{{else}}<p style="margin-top:1.2rem">Brak zgłoszeń.</p>{{end}}
<p class="foot">Widok tylko do odczytu. Daty w strefie Europe/Warsaw. Wycofanie udziału robi uczestnik w swoim panelu; awaryjne usunięcie: <b>dday -delete &lt;handle&gt;</b>.</p>
{{template "foot" .}}{{end}}

{{define "message"}}{{template "head" .}}
<p style="font-size:1.05rem">{{.Message}}</p>
{{if .Detail}}<p>{{.Detail}}</p>{{end}}
{{template "foot" .}}{{end}}
`
