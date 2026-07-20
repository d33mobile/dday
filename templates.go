package main

import "html/template"

// tmpl parses the named templates once at startup.
var tmpl = template.Must(template.New("dday").Parse(templatesSrc))

const templatesSrc = `
{{define "head"}}<!DOCTYPE html><html lang="pl"><head><meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>{{.Title}} · D-Day · Hakierspejs Łódź</title><link rel="stylesheet" href="/style.css">
</head><body class="page"><main class="wrap"><div class="card">
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
<title>{{.Title}} · D-Day · Hakierspejs Łódź</title><link rel="stylesheet" href="/style.css">
</head>
<body class="page"><main class="wrap wrap-wide"><div class="card">
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
