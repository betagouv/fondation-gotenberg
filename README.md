# fondation-gotenberg

[![Deploy on Scalingo](https://cdn.scalingo.com/deploy/button.svg)](https://dashboard.scalingo.com/create/app?source=https://github.com/betagouv/fondation-gotenberg#main)

Le service qui transforme en PDF les documents de
[Fondation](https://github.com/betagouv/fondation) : ordres du jour,
procès-verbaux et notices.

C'est un [Gotenberg](https://gotenberg.dev/) déployé sur Scalingo, derrière un
reverse proxy nginx, sans LibreOffice. L'api Fondation lui envoie le HTML d'un
document et reçoit le PDF. Le rendu est fait par Chrome, comme un "Imprimer en
PDF" dans un navigateur.

## Comment c'est construit

Un conteneur Scalingo sur la stack `scalingo-26`, assemblé par trois
buildpacks dans cet ordre :

1. **APT** installe Google Chrome, qpdf, exiftool et des polices.
2. **Go** compile Gotenberg v8.34.0 depuis les sources.
3. **nginx** sert de reverse proxy : port `8080` sur l'interface privée
   `=>` Gotenberg sur `127.0.0.1:9091`.

Dans le conteneur, `supervisord` lance et surveille les deux processus, nginx
et gotenberg. Il n'est pas le processus 1 : Scalingo lance la commande du
`Procfile` depuis son script `/start`, qui reste le processus 1.

Après la compilation, `bin/go-post-compile` télécharge le binaire pdfcpu et les
données de césure de Chromium, puis supprime les polices inutiles. Il n'y a pas
de pdftk : qpdf et pdfcpu couvrent tous les usages.

## Déployer

Chaque environnement a son app. `fondation-gotenberg-staging` sert l'api de
staging et `fondation-gotenberg-production` celle de production. Un rendu
lancé sur staging ne ralentit donc pas la production et un réglage s'essaie
sur staging avant de partir en production.

Aucune app n'est liée au dépôt GitHub. On déploie avec `git push`, staging
d'abord.

L'app doit être créée dans le projet `incubateur-dinum/default`, celui des
apps de l'api. C'est lui qui porte le Private Network. Créée ailleurs, l'api
ne la joindrait pas.

```bash
scalingo --region osc-secnum-fr1 create fondation-gotenberg-staging \
  --project-id <id du projet incubateur-dinum/default> --remote scalingo-staging
scalingo --region osc-secnum-fr1 -a fondation-gotenberg-staging stacks-set scalingo-26
git push scalingo-staging main

# Le process type s'appelle `app` et non `web` : il n'est pas routé depuis
# Internet. Un nouveau type démarre à zéro conteneur. On le lance donc à la main.
scalingo --region osc-secnum-fr1 -a fondation-gotenberg-staging scale app:1:M
```

Même chose pour la production avec le remote `scalingo-production` et une
taille L (voir [Taille du conteneur](#taille-du-conteneur)).

La présence du fichier `.buildpacks` suffit à activer le mode multi-buildpack.
Pas besoin de `BUILDPACK_URL`.

### Brancher l'api Fondation

Le service n'a pas d'adresse publique. L'api le joint par son nom de domaine
privé, sur le port 8080.

```bash
scalingo -a fondation-gotenberg private-networks-domain-names
```

Ce nom va dans la variable `GOTENBERG_API_URL` de l'api, sans `/` final :

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

### Taille du conteneur

Production en L, staging en M.

Avant les réglages de Chromium ci-dessous, M ne suffisait pas. La mémoire
était au plafond, le swap saturé et une conversion sur cinq finissait en
erreur après 20 ou 30 s. C'est la panne du 7 octobre 2026. En L, un
document de 700 dossiers se convertit en 2 s.

Avec ces réglages, staging tourne en M pour vérifier que la production peut y
descendre. Scalingo supprime progressivement le swap des conteneurs. La
mesure doit donc tenir sans lui.

### Réglages de Chromium

`bin/start-gotenberg` change quatre valeurs par défaut de Gotenberg, pensées
pour un serveur chargé. Fondation en rend quelques-uns par jour, jamais plus
de deux en même temps.

- `--chromium-auto-start=true` : Chromium démarre avec le conteneur et non à
  la première conversion. Scalingo redémarre régulièrement le conteneur et un
  Chromium lancé à froid a déjà dépassé les 20 s permises,
  faisant échouer la conversion de la personne qui attendait son PDF
- `--chromium-max-concurrency=2` : deux conversions en parallèle au plus, au
  lieu de six. C'est un garde-fou pour la mémoire, pas un gain mesuré
- `--chromium-max-queue-size=10` : au-delà de dix conversions en attente,
  Gotenberg répond tout de suite 429 au lieu de laisser la file grandir
- `--chromium-start-timeout=60s` : marge pour relancer Chromium après un
  plantage, au lieu de 20 s. Elle reste sous les 90 s que
  `bin/start-gotenberg` laisse à `/health`

### Traces OpenTelemetry : désactivées

La variable `OTEL_TRACES_EXPORTER=none`, définie dans `.profile.d/gotenberg.sh`,
est indispensable tant qu'aucun
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
Gotenberg le trouve grâce à la variable d'environnement `CHROMIUM_BIN_PATH`,
définie dans `.profile.d/gotenberg.sh`.

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
`$SCALINGO_PRIVATE_HOSTNAME:8080`. Si cette variable n'existe pas, il écoute
sur `127.0.0.1:8080`. Gotenberg reste sur `127.0.0.1:9091`. Les deux ports sont fixes
et ne dépendent plus de `$PORT`, que Scalingo n'injecte de toute façon que
pour un `web`.

Pour exposer publiquement, il faudrait renommer le process en `web` dans le
`Procfile` et remettre `listen <%= ENV['PORT'] %>;` dans `servers.conf.erb`.

### Pourquoi `servers.conf.erb` et non `nginx.conf.erb`

Le nginx-buildpack de Scalingo inclut `nginx.conf.erb` à l'intérieur d'un bloc
`server { }` déjà déclaré. Or `limit_req_zone` et `upstream` doivent être au
niveau `http { }`. `servers.conf.erb` est inclus à ce niveau.

### Orchestration de nginx

Le buildpack fournit `/app/bin/run`, qui génère la configuration et lance nginx
au premier plan. Si nginx meurt, ce script se termine et supervisord le
relance.

## Fichiers

| Fichier               | Rôle                                                |
| --------------------- | --------------------------------------------------- |
| `.buildpacks`         | ordre des buildpacks : APT `=>` Go `=>` nginx       |
| `.profile.d/gotenberg.sh` | variables d'environnement, lues à chaque démarrage |
| `.profile.d/supervisor.sh` | indique à supervisord, un script Python, où trouver son module |
| `Aptfile`             | paquets système et dépôt Google Chrome              |
| `bin/go-post-compile` | télécharge pdfcpu et les données de césure de Chromium, supprime les polices inutiles |
| `bin/go-pre-compile`  | génère `go.sum` s'il est absent                     |
| `bin/start-gotenberg` | règle et lance gotenberg, attend `/health`, puis reste au premier plan pour supervisord |
| `go.mod`, `go.sum`, `main.go` | wrapper minimal du binaire Gotenberg et empreintes de ses dépendances |
| `Procfile`            | `app: supervisord -c supervisord.conf`              |
| `scalingo.json`       | stack et taille du conteneur                        |
| `servers.conf.erb`    | server nginx, rate limit et upstream gotenberg      |
| `supervisord.conf`    | orchestration de nginx (`bin/run`) et de gotenberg  |

## Moteurs PDF

`bin/start-gotenberg` configure chaque opération PDF explicitement. L'option
globale `--pdfengines-engines` est dépréciée dans Gotenberg 8. Dans chaque
liste, le premier moteur est essayé d'abord et le second sert de secours.

- `--pdfengines-convert-engines=pdfcpu` : conversion PDF/A et PDF/UA
- `--pdfengines-encrypt-engines=qpdf,pdfcpu` : chiffrement
- `--pdfengines-merge-engines=qpdf,pdfcpu` : fusion
- `--pdfengines-rotate-engines=pdfcpu` : rotation, qpdf ne le fait pas
- `--pdfengines-split-engines=pdfcpu,qpdf` : découpage
- `--pdfengines-stamp-engines=pdfcpu` : tampon
- `--pdfengines-watermark-engines=pdfcpu` : filigrane, qpdf ne le fait pas

Pour activer ou désactiver d'autres routes Gotenberg, ajuster les options de
`bin/start-gotenberg` :

- `--chromium-disable-routes=true` : désactive complètement Chromium
- `--pdfengines-disable-routes=true` : désactive tous les moteurs PDF
- `--webhook-*` : configuration des webhooks asynchrones

## Régénérer `go.sum`

`go.sum` est commité. Le build vérifie ainsi l'empreinte de chaque dépendance Go.
Après une montée de version de Gotenberg, le régénérer avec la version de Go
exigée par `go.mod`, sans l'installer :

```bash
docker run --rm -v "$PWD":/src -w /src golang:1.26.2 go mod tidy
git add go.mod go.sum
```

`bin/go-pre-compile` le génère encore à la volée s'il manque, par sécurité.
