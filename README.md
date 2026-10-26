# fondation-gotenberg

Le service qui transforme en PDF les documents de
[Fondation](https://github.com/betagouv/fondation) : ordres du jour,
procès-verbaux et notices.

C'est un [Gotenberg](https://gotenberg.dev/) déployé sur Scalingo, sans
LibreOffice. L'api Fondation lui envoie le HTML d'un
document et reçoit le PDF. Le rendu est fait par Chrome, comme un "Imprimer en
PDF" dans un navigateur.

## Comment c'est construit

Un conteneur Scalingo sur la stack `scalingo-26`, assemblé par deux
buildpacks dans cet ordre :

1. **APT** installe qpdf, exiftool, des polices et les bibliothèques de Chrome.
2. **Go** compile Gotenberg v8.38.1 depuis les sources.

Le `Procfile` lance `bin/start-gotenberg`, qui résout l'adresse privée du
conteneur puis s'efface devant Gotenberg (`exec`). Gotenberg écoute sur cette
adresse, port `8080`. C'est le seul processus du conteneur.

Après la compilation, `bin/go-post-compile` télécharge pdfcpu, Chrome et les
données de césure de Chromium puis supprime les polices inutiles. Il n'y a pas
de pdftk puisque qpdf et pdfcpu couvrent tous les usages.

## Déployer

Chaque environnement a son app. `fondation-gotenberg-staging` sert l'api de
staging et `fondation-gotenberg-production` celle de production. Un rendu
lancé sur staging ne ralentit donc pas la production et un réglage s'essaie
sur staging avant de partir en production.

Aucune app n'est liée au dépôt GitHub. On déploie avec `git push`, staging
d'abord.

Les apps sont créées par l'équipe infra de beta.gouv, dans le projet
`incubateur-dinum/default`. C'est celui des apps de l'api et de leur Private
Network. Créée d'abord dans `incubateur-dinum/fondation`, l'app de staging n'y
démarrait pas, le nom privé de son conteneur ne se résolvant pas. L'api ne
l'aurait pas jointe non plus.

```bash
scalingo --region osc-secnum-fr1 -a fondation-gotenberg-staging git-setup --remote scalingo-staging
scalingo --region osc-secnum-fr1 -a fondation-gotenberg-staging stacks-set scalingo-26
git push scalingo-staging main

# Le process type s'appelle `app` et non `web` : il n'est pas routé depuis
# Internet. Un nouveau type démarre à zéro conteneur. On le lance donc à la main
# et on arrête un éventuel `web` déclaré par défaut par un buildpack.
scalingo --region osc-secnum-fr1 -a fondation-gotenberg-staging scale web:0 app:1:M
```

Même chose pour la production avec le remote `scalingo-production`.

Le premier push d'une nouvelle app échoue avec "Task 'web' has not been found
in your 'Procfile'". Le build est pourtant bon. Après le `scale`, on redéploie
le même commit sous forme d'archive :

```bash
git archive --format=tar.gz --prefix=app/ -o /tmp/gotenberg.tar.gz main
scalingo --region osc-secnum-fr1 -a <app> deploy /tmp/gotenberg.tar.gz $(git rev-parse --short main)
```

La présence du fichier `.buildpacks` suffit à activer le mode multi-buildpack.
Pas besoin de `BUILDPACK_URL`.

### Brancher l'api Fondation

Le service n'a pas d'adresse publique. L'api le joint par son nom de domaine
privé, sur le port 8080.

```bash
scalingo --region osc-secnum-fr1 -a <app> private-networks-domain-names
```

Ce nom va dans la variable `GOTENBERG_API_URL` de l'api, sans `/` final :

```
GOTENBERG_API_URL=http://app.ap-<id>.pn-<id>.private-network.internal:8080
```

## Vérifier que ça marche

Les logs de démarrage doivent afficher "Chromium automatically started" puis
"server started on" suivi de l'adresse privée et du port 8080 :

```bash
scalingo --region osc-secnum-fr1 -a <app> logs -n 200
```

Pour tester une conversion, il faut passer par un conteneur d'une autre app du
même Private Network, par exemple l'api, qui connaît déjà l'adresse :

```bash
scalingo --region osc-secnum-fr1 -a fondation-api-staging run bash
> curl $GOTENBERG_API_URL/health
> echo "<html><body>test</body></html>" > index.html
> curl -F files=@index.html $GOTENBERG_API_URL/forms/chromium/convert/html -o out.pdf
> head -c 8 out.pdf   # attendu : %PDF-1.4
```

Pour inspecter le conteneur lui-même :

```bash
scalingo --region osc-secnum-fr1 -a <app> run bash
> which qpdf exiftool pdfcpu
> $CHROMIUM_BIN_PATH --version
> /app/bin/scalingo-gotenberg --help | head -50
```

## À savoir avant de toucher

### Taille du conteneur

Staging et production en M.

Avant les réglages de Chromium ci-dessous, M ne suffisait pas. La mémoire
était au plafond, le swap saturé et une conversion sur cinq finissait en
erreur après 20 ou 30 s. C'est la panne du 7 octobre 2026. L'ancienne app
était donc passée en L.

Avec ces réglages et sans nginx ni superviseur, Gotenberg occupait 134 Mo
après deux rendus sur staging. Les apps créées en octobre 2026 n'ont pas de
swap, que Scalingo supprime progressivement. Sur chacune, une alerte prévient
quand la mémoire dépasse 80 % pendant 3 minutes.

### Une conversion, un contexte de navigateur

Depuis Gotenberg 8.38, chaque conversion a son propre contexte de navigateur,
sans cache partagé avec les précédentes. Une ressource chargée par l'URL d'un
site est donc retéléchargée à chaque rendu. L'api envoie paged.js avec le
document pour cette raison. Toute nouvelle ressource doit suivre le même
chemin ou être intégrée au HTML, comme les polices.

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
- `--chromium-start-timeout=60s` : marge pour lancer ou relancer Chromium,
  au lieu de 20 s

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

### chrome-headless-shell, de Chrome for Testing

Gotenberg pilote `chrome-headless-shell`, la variante de Chrome sans interface
publiée par [Chrome for Testing](https://googlechromelabs.github.io/chrome-for-testing/).
Elle pèse 274 Mo contre 434 Mo pour le Chrome complet. Chaque version est
publiée à une adresse fixe. `bin/go-post-compile` en télécharge une version épinglée et
vérifie son empreinte SHA-256 avant de la décompresser. Le binaire est à
`/app/chrome-headless-shell-linux64/chrome-headless-shell`. Gotenberg le trouve
grâce à la variable `CHROMIUM_BIN_PATH` de `.profile.d/gotenberg.sh`.

Le dépôt APT de Google n'est plus utilisé. Sa signature ne pouvait pas être
vérifiée par le buildpack et il ne garde que les dernières versions de Chrome.
Sur Ubuntu, le paquet `chromium` d'APT est un stub snap, inutilisable dans un
buildpack.

Pour changer de version, prendre celle du canal Stable dans
[last-known-good-versions-with-downloads.json](https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json),
télécharger son archive `chrome-headless-shell-linux64.zip` et en calculer
l'empreinte avec `shasum -a 256`. Puis reporter les deux valeurs dans
`bin/go-post-compile`. Si Chrome refuse de démarrer, une bibliothèque manque.
`ldd chrome-headless-shell | grep "not found"` dans l'image
`scalingo/scalingo-26` la donne, à ajouter à l'`Aptfile`.

### Version de Go

Gotenberg v8.38.1 requiert Go 1.27.1. Si le buildpack Go de Scalingo ne la
prend pas en charge, ajouter la directive `toolchain` à `go.mod` ou revenir à
une version antérieure de Gotenberg.

### Aucun port exposé sur Internet

Le process type s'appelle `app` et non `web`. Le routeur public de Scalingo ne
lui envoie donc aucun trafic. Gotenberg écoute sur l'adresse privée du
conteneur, port `8080`. Il n'accepte qu'une adresse IP, d'où la résolution de
`SCALINGO_PRIVATE_HOSTNAME` par `bin/start-gotenberg`. Si le nom ne se résout
pas, le script s'arrête en erreur plutôt que d'écouter ailleurs. Un service
injoignable qui a l'air de fonctionner serait pire qu'une panne visible.

### Ni nginx ni superviseur

Si Gotenberg s'arrête, le conteneur s'arrête avec lui. Scalingo le redémarre et
l'inscrit dans la timeline de l'app. Jusqu'en octobre 2026, supervisord
relançait Gotenberg dans le conteneur. Le 27/07/2026, il l'a abandonné deux
fois après trois échecs de démarrage. Le conteneur restait "running" et nginx
répondait "ok" sur `/health`, alors qu'aucun PDF ne sortait. `/health` est
désormais celui de Gotenberg, qui vérifie aussi Chromium.

## Fichiers

| Fichier               | Rôle                                                |
| --------------------- | --------------------------------------------------- |
| `.buildpacks`         | ordre des buildpacks : APT `=>` Go                  |
| `.profile.d/gotenberg.sh` | variables d'environnement, lues à chaque démarrage |
| `Aptfile`             | paquets système et bibliothèques de Chrome          |
| `bin/go-post-compile` | télécharge pdfcpu, Chrome et les données de césure de Chromium, supprime les polices inutiles |
| `bin/go-pre-compile`  | génère `go.sum` s'il est absent                     |
| `bin/start-gotenberg` | résout l'adresse privée puis lance gotenberg avec ses réglages |
| `go.mod`, `go.sum`, `main.go` | wrapper minimal du binaire Gotenberg et empreintes de ses dépendances |
| `Procfile`            | `app: bin/start-gotenberg`                          |
| `scalingo.json`       | stack et taille du conteneur                        |

## Moteurs PDF

`bin/start-gotenberg` donne ses moteurs à chaque opération PDF, l'option
globale `--pdfengines-engines` étant dépréciée dans Gotenberg 8. Le premier
moteur de chaque liste est essayé d'abord. Fondation n'utilise que la
conversion HTML de Chromium.

## Régénérer `go.sum`

`go.sum` est commité. Le build vérifie ainsi l'empreinte de chaque dépendance Go.
Après une montée de version de Gotenberg, le régénérer avec la version de Go
exigée par `go.mod`, sans l'installer :

```bash
docker run --rm -v "$PWD":/src -w /src golang:1.27.1 go mod tidy
git add go.mod go.sum
```

`bin/go-pre-compile` le génère encore à la volée s'il manque, par sécurité.
