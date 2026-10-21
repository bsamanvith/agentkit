# A skill body of exactly 151 lines, one over the limit.
f=.agents/skills/alpha/SKILL.md
printf '%s\n' --- 'name: alpha' 'description: A fixture skill one line too long.' --- '# alpha' >"$f"
i=2
while [ "$i" -le 151 ]; do
	echo "line $i"
	i=$((i + 1))
done >>"$f"
