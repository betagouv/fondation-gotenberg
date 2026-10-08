# fondation-gotenberg

[![Deploy on Scalingo](https://cdn.scalingo.com/deploy/button.svg)](https://dashboard.scalingo.com/create/app?source=https://github.com/betagouv/fondation-gotenberg#main)

Le service qui transforme en PDF les documents de Fondation : ordres du jour,
procès-verbaux et notices.

C'est un [Gotenberg](https://gotenberg.dev/) déployé sur Scalingo, derrière un
reverse proxy nginx, sans LibreOffice. L'api Fondation lui envoie le HTML d'un
document et reçoit le PDF. Le rendu est fait par Chrome, comme un "Imprimer en
PDF" dans un navigateur.

## Comment c'est construit

Un conteneur Scalingo sur la stack `scalingo-24`, assemblé par trois
buildpacks, dans cet ordre :

1. **APT** installe Google Chrome, qpdf, exiftool et des polices.
2. **Go** compile Gotenberg v8.34.0 depuis les sources.
3. **nginx** fait reverse proxy : port `8080` sur l'interface privée
   `=>` Gotenberg sur `127.0.0.1:9091`.

Dans le conteneur, `supervisord` lance et surveille les deux processus, nginx
et gotenberg. Il n'est pas le processus 1 : Scalingo lance la commande du
`Procfile` depuis son script `/start`, qui garde ce rôle.

Après la compilation, `bin/go-post-compile` télécharge le binaire pdfcpu et les
données de césure de Chromium, puis allège les polices. Il n'y a pas de pdftk :
qpdf et pdfcpu couvrent tous les usages.

## Déployer

```bash
scalingo create fondation-gotenberg --stack scalingo-24
git push scalingo main

# Le process type s'appelle `app` et non `web` : il n'est pas routé depuis
# Internet. Un nouveau type démarre à zéro conteneur, on le lance en L.
scalingo -a fondation-gotenberg scale app:1:L
```

Le fichier `.buildpacks` active le mode multi-buildpack tout seul. Pas besoin
de `BUILDPACK_URL`.

### Brancher l'api Fondation

Le service n'a pas d'adresse publique. L'api le joint par son nom de domaine
privé, sur le port 8080.

```bash
scalingo -a fondation-gotenberg private-networks-domain-names
```

Ce nom se pose dans la variable `GOTENBERG_API_URL` de l'api, sans `/` final :

```
GOTENBERG_API_URL=http://app.ap-<id>.pn-<id>.private-network.internal:8080
```

## Vérifier que ça marche

Les logs de démarrage doivent afficher "gotenberg est healthy" :

```bash
scalingo -a fondation-gotenberg logs -n 200
```

Pour tester une conversion, il faut passer par un conteneur d'une autre app du
même Private Network, par exemple l'api, qui connaît déjà l'adresse :

```bash
scalingo -a fondation-api-staging run bash
> curl $GOTENBERG_API_URL/health
> echo "<html><body>test</body></html>" > index.html
> curl -F files=@index.html $GOTENBERG_API_URL/forms/chromium/convert/html -o out.pdf
> head -c 8 out.pdf   # attendu : %PDF-1.4
```

Pour inspecter le conteneur lui-même :

```bash
scalingo -a fondation-gotenberg run bash
> which qpdf exiftool pdfcpu
> ls -la /app/.apt/opt/google/chrome/
> /app/bin/scalingo-gotenberg --help | head -50
```

## À savoir avant de toucher

### Taille du conteneur : L au minimum

En M (512 Mio), Chromium n'a pas la place de démarrer. La mémoire est au
plafond, le swap saturé et toutes les conversions finissent en 503 "context
deadline exceeded". C'est la panne du 7 octobre 2026.

En L, un document de 700 dossiers se convertit en 2 s.

### Traces OpenTelemetry : désactivées

La variable `OTEL_TRACES_EXPORTER=none` est indispensable tant qu'aucun
collecteur n'accepte les traces. Sans elle, Gotenberg vise `localhost:4318` et
logue une erreur après chaque conversion. L'instance `sentry.incubateur.net` ne
reçoit pas de traces OTLP et répond 403.

### Délai par requête : 30 s

Gotenberg abandonne une conversion après 30 s, sa valeur par défaut pour
`--api-timeout`. Le plus gros document de Fondation se convertit en 2 s, la
marge est large.

### Chrome plutôt que Chromium

Sur Ubuntu 24.04, le paquet `chromium` d'APT est un stub snap, inutilisable
dans un buildpack. On installe `google-chrome-stable` depuis le dépôt officiel
Google. Le binaire est à `/app/.apt/opt/google/chrome/google-chrome`.
Gotenberg le trouve par la variable d'environnement `CHROMIUM_BIN_PATH`, posée
dans `scalingo.json`.

### Version de Go

Gotenberg v8.34.0 requiert Go 1.26.2. Si le buildpack Go de Scalingo ne la
prend pas en charge, ajouter la directive `toolchain` à `go.mod` ou revenir à
une version antérieure de Gotenberg.

### Taille du slug

Le build télécharge environ 200 Mo d'archives APT, dont 141 Mo pour Chrome.
Installé, `/app` pèse environ 900 Mo : 434 Mo pour Chrome et 108 Mo de polices.
L'image du dernier déploiement fait 885 Mio sur Scalingo. Chiffres relevés le
8 octobre 2026.

### Aucun port exposé sur Internet

Le process type s'appelle `app` et non `web`. Le routeur public de Scalingo ne
lui envoie donc aucun trafic. nginx écoute sur
`$SCALINGO_PRIVATE_HOSTNAME:8080`, avec repli sur `127.0.0.1:8080`. Gotenberg
reste sur `127.0.0.1:9091`. Les deux ports sont fixes, plus aucune dépendance à
`$PORT`, qui n'est de toute façon injecté que pour un `web`.

Pour exposer publiquement, il faudrait renommer le process en `web` dans le
`Procfile` et remettre `listen <%= ENV['PORT'] %>;` dans `servers.conf.erb`.

### Pourquoi `servers.conf.erb` et non `nginx.conf.erb`

Le nginx-buildpack de Scalingo inclut `nginx.conf.erb` à l'intérieur d'un bloc
`server { }` déjà déclaré. Or `limit_req_zone` et `upstream` doivent être au
niveau `http { }`. `servers.conf.erb` est inclus à ce niveau.

### Orchestration de nginx

Le buildpack fournit `/app/bin/run`, qui rend la configuration et lance nginx
au premier plan. Si nginx meurt, ce script sort et supervisord le relance.

## Fichiers

| Fichier               | Rôle                                                |
| --------------------- | --------------------------------------------------- |
| `.buildpacks`         | ordre des buildpacks : APT `=>` Go `=>` nginx       |
| `Aptfile`             | paquets système et dépôt Google Chrome              |
| `go.mod`, `main.go`   | wrapper minimal du binaire Gotenberg                |
| `bin/go-pre-compile`  | génère `go.sum` s'il est absent                     |
| `bin/go-post-compile` | télécharge pdfcpu et la césure Chromium, allège les polices |
| `bin/start-gotenberg` | lance gotenberg, attend `/health`, tient la main    |
| `.profile.d/supervisor.sh` | donne à supervisord, script Python, l'accès à son module |
| `servers.conf.erb`    | server nginx, rate limit et upstream gotenberg      |
| `supervisord.conf`    | orchestration de nginx (`bin/run`) et de gotenberg  |
| `Procfile`            | `app: supervisord -c supervisord.conf`              |
| `scalingo.json`       | taille du conteneur et variables d'environnement    |

## Moteurs PDF

`bin/start-gotenberg` configure chaque opération PDF explicitement. L'option
globale `--pdfengines-engines` est dépréciée dans Gotenberg 8. Dans chaque
liste, l'ordre est l'ordre de repli.

- `--pdfengines-merge-engines=qpdf,pdfcpu` : fusion
- `--pdfengines-split-engines=pdfcpu,qpdf` : découpage
- `--pdfengines-encrypt-engines=qpdf,pdfcpu` : chiffrement
- `--pdfengines-watermark-engines=pdfcpu` : filigrane, qpdf ne le fait pas
- `--pdfengines-rotate-engines=pdfcpu` : rotation, qpdf ne le fait pas
- `--pdfengines-stamp-engines=pdfcpu` : tampon
- `--pdfengines-convert-engines=pdfcpu` : conversion PDF/A et PDF/UA

Pour activer ou désactiver d'autres routes Gotenberg, ajuster les options de
`bin/start-gotenberg` :

- `--chromium-disable-routes=true` : désactive complètement Chromium
- `--pdfengines-disable-routes=true` : désactive tous les moteurs PDF
- `--webhook-*` : configuration des webhooks asynchrones

## Générer `go.sum` en local

`bin/go-pre-compile` génère `go.sum` à la volée s'il est absent, en appelant
`go mod tidy` sur le conteneur de build. C'est un filet de sécurité. Pour un
build reproductible, mieux vaut le générer en local et le commiter :

```bash
cd fondation-gotenberg
go mod tidy
git add go.sum
```
