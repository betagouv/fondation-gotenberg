# fondation-gotenberg

[![Deploy on Scalingo](https://cdn.scalingo.com/deploy/button.svg)](https://dashboard.scalingo.com/create/app?source=https://github.com/betagouv/fondation-gotenberg#main)

Déploiement de [Gotenberg](https://gotenberg.dev/) sur Scalingo derrière un
reverse proxy nginx, sans LibreOffice (route désactivée).

## Architecture

Un dyno Scalingo `scalingo-24` avec :

- **APT buildpack** — installe Google Chrome, qpdf, exiftool, fonts…
- **Go buildpack** — compile Gotenberg v8.34.0 depuis les sources
- **nginx buildpack** — reverse proxy `:8080` (interface privée) → `127.0.0.1:9091`
- **supervisord** — orchestre `nginx` + `gotenberg`. Il n'est pas PID 1 :
  Scalingo lance la commande du `Procfile` depuis son script `/start`, qui reste
  le processus 1 du conteneur.

Le binaire pdfcpu est téléchargé par `bin/go-post-compile` (hook du Go
buildpack). Pas de pdftk — les moteurs `qpdf` + `pdfcpu` couvrent tous les
usages configurés dans `bin/start-gotenberg`.

## Déploiement

```bash
scalingo create fondation-gotenberg --stack scalingo-24
git push scalingo main
# Le process type s'appelle `app` (et non `web`) : il n'est donc PAS routé
# depuis Internet. On le démarre explicitement (les nouveaux types démarrent à 0) :
scalingo -a fondation-gotenberg scale app:1:L
```

L'api Fondation joint le service par son nom de domaine privé, sur le port 8080.
Il s'obtient avec `scalingo -a fondation-gotenberg private-networks-domain-names`
et se pose dans la variable `GOTENBERG_API_URL` de l'api, sans `/` final :

```
GOTENBERG_API_URL=http://app.ap-<id>.pn-<id>.private-network.internal:8080
```

> **Non exposé sur Internet.** Le process type est nommé `app` au lieu de `web`,
> donc le routeur public Scalingo ne lui envoie aucun trafic. nginx écoute sur
> `$SCALINGO_PRIVATE_HOSTNAME:8080` (Private Network) avec repli sur
> `127.0.0.1:8080`. Pour ré-exposer publiquement : renommer le process en `web`
> dans le `Procfile` et remettre `listen <%= ENV['PORT'] %>;` dans
> `servers.conf.erb`.

Le fichier `.buildpacks` déclenche automatiquement le mode multi-buildpack
(pas besoin de `BUILDPACK_URL`).

## Vérification

```bash
# 1. Logs de démarrage
scalingo -a fondation-gotenberg logs -n 200

# 2. Health check et conversion. L'app n'a pas d'adresse publique : on passe
#    par un conteneur d'une autre app du même Private Network, par exemple
#    l'api Fondation, qui connaît déjà l'adresse dans GOTENBERG_API_URL.
scalingo -a fondation-api-staging run bash
> curl $GOTENBERG_API_URL/health
> echo "<html><body>test</body></html>" > index.html
> curl -F files=@index.html $GOTENBERG_API_URL/forms/chromium/convert/html -o out.pdf
> head -c 8 out.pdf   # attendu : %PDF-1.4

# 3. Inspection en session bash
scalingo -a fondation-gotenberg run bash
> which qpdf exiftool pdfcpu
> ls -la /app/.apt/opt/google/chrome/
> /app/bin/scalingo-gotenberg --help | head -50
```

## Fichiers clés

| Fichier              | Rôle                                              |
| -------------------- | ------------------------------------------------- |
| `.buildpacks`        | ordre des buildpacks (APT → Go → nginx)           |
| `Aptfile`            | paquets système + dépôt Google Chrome             |
| `go.mod`, `main.go`  | wrapper minimal du binaire Gotenberg              |
| `bin/go-pre-compile` | génère `go.sum` s'il est absent                   |
| `bin/go-post-compile`| télécharge le binaire pdfcpu                      |
| `bin/start-gotenberg`| lance gotenberg, attend /health, tient la main    |
| `servers.conf.erb`   | server nginx + rate limit + upstream gotenberg    |
| `supervisord.conf`   | orchestration nginx (`bin/run`) + gotenberg       |
| `Procfile`           | `app: supervisord -c supervisord.conf` (non-`web`)|

## Points d'attention

- **Chromium sur Ubuntu 24.04** — le paquet `chromium` d'APT est un stub snap
  inutilisable dans un buildpack. On utilise `google-chrome-stable` via le
  dépôt officiel Google (`dl.google.com`). Le binaire est à
  `/app/.apt/opt/google/chrome/google-chrome`. Gotenberg le trouve par la
  variable d'environnement `CHROMIUM_BIN_PATH`, posée dans `scalingo.json`.
- **Version Go** — Gotenberg v8.34.0 requiert Go 1.26.2. Vérifier que le
  buildpack Go Scalingo prend en charge cette version ; sinon, ajouter la
  directive `toolchain` à `go.mod` ou downgrade Gotenberg.
- **Taille du slug**. Le build télécharge environ 200 Mo d'archives APT, dont
  141 Mo pour Chrome. Une fois installé, `/app` pèse environ 900 Mo : 434 Mo
  pour Chrome et 108 Mo de polices. L'image du dernier déploiement fait 885 Mio
  sur Scalingo. Chiffres relevés le 8 octobre 2026.
- **Taille du conteneur**. Il faut un conteneur `L` au minimum. En `M` (512 Mio),
  Chromium n'a pas la place de démarrer, la mémoire est au plafond, le swap
  saturé et toutes les conversions finissent en 503 "context deadline exceeded".
  C'est la panne du 7 octobre 2026. En `L`, un document de 700 dossiers se
  convertit en 2 s.
- **Traces OpenTelemetry**. La variable `OTEL_TRACES_EXPORTER=none` est
  indispensable tant qu'aucun collecteur n'accepte les traces. Sans elle,
  Gotenberg vise `localhost:4318` et logue une erreur après chaque conversion.
  L'instance `sentry.incubateur.net` ne reçoit pas de traces OTLP et répond 403.
- **Délai par requête**. Gotenberg abandonne une conversion après 30 s, sa
  valeur par défaut pour `--api-timeout`. Le plus gros document de Fondation
  se convertit en 2 s, la marge est large.
- **Aucun port exposé sur Internet** — le process type `app` (≠ `web`/`tcp`)
  n'est pas routé par le front public Scalingo. nginx bind
  `$SCALINGO_PRIVATE_HOSTNAME:8080` (repli `127.0.0.1:8080`), Gotenberg reste
  sur `127.0.0.1:9091`. Les deux ports sont fixes et distincts, plus aucune
  dépendance à `$PORT` (qui n'est de toute façon injecté que pour un `web`).
- **Choix `servers.conf.erb` vs `nginx.conf.erb`** — le nginx-buildpack
  Scalingo inclut `nginx.conf.erb` **à l'intérieur** d'un `server { }` déjà
  déclaré. Or `limit_req_zone` doit être au niveau `http { }`. On utilise
  donc `servers.conf.erb`, inclus au niveau `http`, ce qui nous permet
  aussi de définir un `upstream`.
- **Orchestration nginx** — le buildpack expose `/app/bin/run` qui rend la
  config et lance nginx en foreground. Ce script sort si nginx meurt ;
  supervisord le relance.

## Génération de `go.sum` en local (recommandé)

Le script `bin/go-pre-compile` génère `go.sum` à la volée si absent (en
appelant `go mod tidy` sur le dyno de build). C'est un filet de sécurité :
en pratique, mieux vaut le générer **localement** et le committer pour
avoir un build reproductible :

```bash
cd fondation-gotenberg
go mod tidy
git add go.sum
```

## Moteurs PDF

`bin/start-gotenberg` configure explicitement chaque opération PDF (l'option
globale `--pdfengines-engines` est dépréciée dans Gotenberg 8) :

- `--pdfengines-merge-engines=qpdf,pdfcpu` : fusion
- `--pdfengines-split-engines=pdfcpu,qpdf` : découpage
- `--pdfengines-encrypt-engines=qpdf,pdfcpu` : chiffrement
- `--pdfengines-watermark-engines=pdfcpu` : filigrane (qpdf ne le fait pas)
- `--pdfengines-rotate-engines=pdfcpu` : rotation (qpdf ne le fait pas)
- `--pdfengines-stamp-engines=pdfcpu` : tampon
- `--pdfengines-convert-engines=pdfcpu` : conversion PDF/A et PDF/UA

L'ordre dans chaque liste = ordre de fallback. Aucune route ne fait appel à
pdftk, donc pas besoin de JRE.

## Configuration alternative

Pour activer / désactiver d'autres routes Gotenberg, ajuster les options de
`bin/start-gotenberg`. Options utiles :

- `--chromium-disable-routes=true` — désactive complètement Chromium
- `--pdfengines-disable-routes=true` — désactive tous les moteurs PDF
- `--webhook-*` — configuration des webhooks asynchrones
