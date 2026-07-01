#!/usr/bin/env bash
# Run inside L1. Installs fish, sets a green Kali-style prompt, makes it default.
# Its sole purpose is to have a useful clean yet different prompt for L1
set -euo pipefail
U="${1:-$(whoami)}"

sudo apt-get update -y
sudo apt-get install -y fish

D="/home/$U/.config/fish"
sudo -u "$U" mkdir -p "$D"
sudo -u "$U" tee "$D/config.fish" >/dev/null <<'EOF'
if status is-interactive
    set -g fish_greeting ""
    alias ls='ls --color=auto'
    alias grep='grep --color=auto'
end
function fish_prompt
    set -l s $status
    set -l g (set_color -o brgreen); set -l b (set_color blue)
    set -l r (set_color red); set -l n (set_color normal)
    echo -n $g'┌──('$n(whoami)'@'(prompt_hostname)$g')-['$b(prompt_pwd)$g']'$n
    echo
    test $s -eq 0; and echo -n $g'└─$ '$n; or echo -n $r'└─$ '$n
end
EOF

grep -qx /usr/bin/fish /etc/shells || echo /usr/bin/fish | sudo tee -a /etc/shells >/dev/null
sudo chsh -s /usr/bin/fish "$U"
echo "Done. Re-SSH to load fish."