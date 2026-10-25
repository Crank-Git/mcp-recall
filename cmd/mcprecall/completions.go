// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// cmd/mcprecall/completions.go

package main

import "fmt"

func completionScript(shell string) (string, error) {
	switch shell {
	case "bash":
		return bashCompletion, nil
	case "zsh":
		return zshCompletion, nil
	case "fish":
		return fishCompletion, nil
	default:
		return "", fmt.Errorf("unknown shell %q. Supported: bash, zsh, fish", shell)
	}
}

const bashCompletion = `# mcprecall bash completions
# Add to your ~/.bashrc or source from /etc/bash_completion.d/mcprecall

_mcprecall() {
  local cur prev
  COMPREPLY=()
  cur="${COMP_WORDS[COMP_CWORD]}"

  local commands="install uninstall status server gc discover profiles learn import completions --help --version"
  local profiles_cmds="list available info install update remove seed feed check retrain test"

  if [[ ${COMP_CWORD} -eq 1 ]]; then
    COMPREPLY=( $(compgen -W "${commands}" -- "${cur}") )
    return 0
  fi

  if [[ "${COMP_WORDS[1]}" == "profiles" ]]; then
    if [[ ${COMP_CWORD} -eq 2 ]]; then
      COMPREPLY=( $(compgen -W "${profiles_cmds}" -- "${cur}") )
      return 0
    fi
    if [[ ${COMP_CWORD} -ge 3 ]]; then
      local subcmd="${COMP_WORDS[2]}"
      if [[ "$subcmd" == "install" || "$subcmd" == "remove" || "$subcmd" == "test" ]]; then
        local profile_ids
        profile_ids="$(mcprecall profiles list --machine-readable 2>/dev/null)"
        COMPREPLY=( $(compgen -W "${profile_ids}" -- "${cur}") )
        return 0
      fi
    fi
  fi

  if [[ "${COMP_WORDS[1]}" == "completions" ]]; then
    COMPREPLY=( $(compgen -W "bash zsh fish" -- "${cur}") )
    return 0
  fi
}

complete -F _mcprecall mcprecall
`

const zshCompletion = `#compdef mcprecall
# mcprecall zsh completions
# Add to your fpath, e.g.: mcprecall completions zsh > ~/.zfunc/_mcprecall
# Then in ~/.zshrc: fpath=(~/.zfunc ${fpath}); autoload -Uz compinit && compinit

_mcprecall_profiles() {
  local state
  _arguments '1: :->subcommand' '*:: :->args'
  case $state in
    subcommand)
      local subcommands=(
        'list:show installed profiles'
        'available:browse the community catalog'
        'info:show full metadata for a profile'
        'install:install a community profile by ID'
        'update:update all installed community profiles'
        'remove:remove an installed community profile'
        'seed:install profiles for detected MCPs'
        'feed:contribute a local profile to the community'
        'check:detect pattern conflicts'
        'retrain:suggest profile improvements from stored data'
        'test:test a profile against real input'
      )
      _describe 'subcommand' subcommands
      ;;
    args)
      case $words[1] in
        install|remove|test)
          local profiles
          profiles=(${(f)"$(mcprecall profiles list --machine-readable 2>/dev/null)"})
          _describe 'profile' profiles
          ;;
        seed)
          _arguments '--all[install every profile in the community catalog]'
          ;;
      esac
      ;;
  esac
}

_mcprecall() {
  local state
  _arguments \
    '(-h --help)'{-h,--help}'[show help and exit]' \
    '(-v --version)'{-v,--version}'[show version and exit]' \
    '1: :->command' \
    '*:: :->args'
  case $state in
    command)
      local commands=(
        'install:register hooks and MCP server in Claude Code'
        'uninstall:remove hooks and MCP server'
        'status:show current configuration and health'
        'gc:reclaim disk from orphaned project databases'
        'discover:rank output routed to fallback handlers'
        'server:run the recall MCP server'
        'profiles:manage compression profiles'
        'learn:generate profile suggestions from installed MCPs'
        'import:restore items from a recall__export dump'
        'completions:print shell completion script (bash, zsh, fish)'
      )
      _describe 'command' commands
      ;;
    args)
      case $words[1] in
        profiles) _mcprecall_profiles ;;
        completions)
          local shells=('bash:bash completion' 'zsh:zsh completion' 'fish:fish completion')
          _describe 'shell' shells
          ;;
      esac
      ;;
  esac
}

_mcprecall "$@"
`

const fishCompletion = `# mcprecall fish completions
# Save to: mcprecall completions fish > ~/.config/fish/completions/mcprecall.fish

set -l commands install uninstall status server gc discover profiles learn import completions

complete -c mcprecall -f -n "not __fish_seen_subcommand_from $commands" -a install -d "Register hooks and MCP server"
complete -c mcprecall -f -n "not __fish_seen_subcommand_from $commands" -a uninstall -d "Remove hooks and MCP server"
complete -c mcprecall -f -n "not __fish_seen_subcommand_from $commands" -a status -d "Show configuration and health"
complete -c mcprecall -f -n "not __fish_seen_subcommand_from $commands" -a gc -d "Reclaim disk from orphaned project databases"
complete -c mcprecall -f -n "not __fish_seen_subcommand_from $commands" -a discover -d "Rank output routed to fallback handlers"
complete -c mcprecall -f -n "not __fish_seen_subcommand_from $commands" -a server -d "Run the recall MCP server"
complete -c mcprecall -f -n "not __fish_seen_subcommand_from $commands" -a profiles -d "Manage compression profiles"
complete -c mcprecall -f -n "not __fish_seen_subcommand_from $commands" -a learn -d "Generate profile suggestions"
complete -c mcprecall -f -n "not __fish_seen_subcommand_from $commands" -a import -d "Restore items from an export dump"
complete -c mcprecall -f -n "not __fish_seen_subcommand_from $commands" -a completions -d "Print shell completion script"
complete -c mcprecall -f -n "not __fish_seen_subcommand_from $commands" -s h -l help -d "Show help"
complete -c mcprecall -f -n "not __fish_seen_subcommand_from $commands" -s v -l version -d "Show version"

complete -c mcprecall -f -n "__fish_seen_subcommand_from completions" -a "bash zsh fish"

set -l profile_cmds list available info install update remove seed feed check retrain test
complete -c mcprecall -f -n "__fish_seen_subcommand_from profiles; and not __fish_seen_subcommand_from $profile_cmds" -a "$profile_cmds"
complete -c mcprecall -f -n "__fish_seen_subcommand_from profiles; and __fish_seen_subcommand_from install remove test" -a "(mcprecall profiles list --machine-readable 2>/dev/null)"
complete -c mcprecall -n "__fish_seen_subcommand_from profiles; and __fish_seen_subcommand_from seed" -l all -d "Install every profile in the catalog"
`
