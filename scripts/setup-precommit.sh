#!/bin/sh

GIT_ROOT=$(git rev-parse --show-toplevel)
GIT_HOOKS_PATH=$(git rev-parse --git-path hooks)
PRE_COMMIT_FILE="${GIT_HOOKS_PATH}/pre-commit"

echo "=========================================="
echo " Setup Git Hooks"
echo " Location: ${PRE_COMMIT_FILE}"
echo "=========================================="

# Backup si ya existe
if [ -f "$PRE_COMMIT_FILE" ]; then
   echo "⚠️  Hook existente detectado. Creando backup..."
   cp "$PRE_COMMIT_FILE" "${PRE_COMMIT_FILE}.bak"
fi


cat <<EOF > "$PRE_COMMIT_FILE"
#!/bin/bash
set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

printf "${BLUE}⚡ Running pre-commit checks...${NC}\n"

fail() {
    printf "${RED}❌ %s${NC}\n" "$1"
    exit 1
}

printf "Checking format... "
if ! make check-format >/dev/null 2>&1; then
    printf "${RED}FAILED${NC}\n"
    fail "Code format issues found. Run '${YELLOW}make format${NC}' and stage changes."
else
    printf "${GREEN}OK${NC}\n"
fi

printf "Running linter... "
# Ejecutar lint sin capturar output para evitar problemas con caracteres especiales
if ! make lint >/dev/null 2>&1; then
    printf "${RED}FAILED${NC}\n"
    printf "\n${YELLOW}💡 Run 'make lint' to see detailed errors.${NC}\n"
    fail "Linter failed. Fix errors manually."
fi

if ! git diff --quiet; then
    printf "${YELLOW}CHANGED${NC}\n"
    printf "${YELLOW}⚠️  'make lint' auto-fixed files, but they are not staged.${NC}\n"
    fail "Please run '${YELLOW}git add .${NC}' and commit again."
fi
printf "${GREEN}OK${NC}\n"

printf "Checking vulnerabilities... "
if ! make vulncheck >/dev/null 2>&1; then
    printf "${RED}FAILED${NC}\n"
    fail "Security vulnerabilities found! Run '${YELLOW}make vulncheck${NC}'."
else
    printf "${GREEN}OK${NC}\n"
fi

printf "${GREEN}✅ All checks passed!${NC}\n"
exit 0


EOF

chmod +x "${PRE_COMMIT_FILE}"

echo "✅ Hook instalado correctamente en ${PRE_COMMIT_FILE}"
echo "   (Backup guardado en ${PRE_COMMIT_FILE}.bak)"