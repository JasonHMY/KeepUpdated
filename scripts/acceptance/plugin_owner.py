#!/usr/bin/env python3
"""Exercise the built CLI and a real Claude binary in a disposable home.

No session, account, remote registry, host plugin, or global configuration is used.
The synthetic Git marketplace models a previously fetched public catalog.
"""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--cli', required=True, type=Path)
    parser.add_argument('--claude', required=True, type=Path)
    args = parser.parse_args()
    cli, claude = args.cli.resolve(strict=True), args.claude.resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix='keepupdated-plugin-') as temporary:
        home = Path(temporary).resolve() / 'home'
        home.mkdir()
        config = home / '.claude'
        market = config / 'plugins/marketplaces/fixture-market'
        source = market / 'example'
        cache = config / 'plugins/cache/fixture-market/example'
        environment = {
            'HOME': str(home), 'CLAUDE_CONFIG_DIR': str(config),
            'PATH': '/usr/bin:/bin', 'LC_ALL': 'C',
            'DISABLE_AUTOUPDATER': '1', 'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC': '1',
            'GIT_ALLOW_PROTOCOL': '', 'GIT_CONFIG_NOSYSTEM': '1',
            'GIT_CONFIG_GLOBAL': '/dev/null', 'GIT_TERMINAL_PROMPT': '0',
        }

        def write(path, value):
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(json.dumps(value) if isinstance(value, dict) else value)

        def run(command, allowed=(0,)):
            result = subprocess.run([str(x) for x in command], cwd=home, env=environment,
                                    stdin=subprocess.DEVNULL, capture_output=True, text=True,
                                    timeout=120)
            if result.returncode not in allowed:
                raise RuntimeError(f'{Path(command[0]).name} failed ({result.returncode}): {result.stderr}')
            return result.stdout

        def native(*command):
            return json.loads(run([claude, 'plugin', *command]).strip().splitlines()[-1])

        def git(*command):
            return run(['/usr/bin/git', '-C', market, *command]).strip()

        def commit():
            git('add', '.')
            git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid',
                'commit', '-m', 'Fixture plugin version')
            git('update-ref', 'refs/remotes/origin/main', 'HEAD')
            return git('rev-parse', 'HEAD')

        write(market / '.claude-plugin/marketplace.json', {
            'name': 'fixture-market', 'owner': {'name': 'Fixture'},
            'plugins': [{'name': 'example', 'source': './example'}],
        })
        write(source / '.claude-plugin/plugin.json', {'name': 'example', 'version': '1.0.0'})
        write(source / 'skills/demo/SKILL.md', '---\nname: demo\ndescription: Fixture\n---\nOld.\n')
        write(source / 'skills/other/SKILL.md', '---\nname: other\ndescription: Fixture\n---\nOther.\n')
        git('init', '-b', 'main')
        git('remote', 'add', 'origin', 'https://github.com/example/fixture-market.git')
        old_commit = commit()
        write(config / 'plugins/known_marketplaces.json', {'fixture-market': {
            'source': {'source': 'git', 'url': 'https://github.com/example/fixture-market.git'},
            'installLocation': str(market), 'lastUpdated': '2026-10-19T00:00:00Z',
        }})
        write(config / 'settings.json', {})
        receipt = native('install', 'example@fixture-market', '--scope', 'user', '--json')
        assert receipt['outcome'] == 'ok'
        installed = json.loads(run([claude, 'plugin', 'list', '--json']))
        assert len(installed) == 1 and installed[0]['version'] == '1.0.0'
        assert not installed[0].get('readFromFolder')
        manager_link = home / '.local/bin/claude'
        manager_link.parent.mkdir(parents=True)
        manager_link.symlink_to(claude)
        write(source / '.claude-plugin/plugin.json', {'name': 'example', 'version': '1.1.0'})
        write(source / 'skills/demo/SKILL.md', '---\nname: demo\ndescription: Fixture\n---\nNew.\n')
        new_commit = commit()
        inventory = json.loads(run([cli, 'scan', '--format', 'json'], allowed=(0, 1)))
        skill_ids = [x['id'] for x in inventory['items'] if x.get('plugin', {}).get('id') == 'example@fixture-market']
        assert len(skill_ids) == 2
        selection = [argument for item_id in skill_ids for argument in ('--id', item_id)]
        plan = json.loads(run([cli, 'plan', '--action', 'update', '--cached-plugins', *selection]))
        ready = [x for x in plan['items'] if x['status'] == 'ready']
        assert len(ready) == 1, [x['reason'] for x in plan['items']]
        assert ready[0]['plugin_update']['commit'] == new_commit
        assert ready[0]['plugin_update']['owner']['commit'] == old_commit
        plan_file = home / 'plan.json'
        write(plan_file, plan)
        try:
            applied = json.loads(run([cli, 'apply', '--plan', plan_file, '--confirm', plan['plan_id']]))
        except RuntimeError:
            replanned = json.loads(run([cli, 'plan', '--action', 'update', '--cached-plugins', *selection]))
            def diff(a, b, prefix=''):
                if isinstance(a, dict) and isinstance(b, dict):
                    for key in a.keys() | b.keys():
                        diff(a.get(key), b.get(key), prefix + '.' + key)
                elif a != b:
                    print('Changed plan field', prefix, repr(a), repr(b), flush=True)
            for left, right in zip(plan['items'], replanned['items']):
                diff(left, right)
            raise
        result = next(x for x in applied['results'] if x['status'] == 'updated')
        assert result['after_version'] == '1.1.0' and result['reload_required']
        assert result['reason'] == 'plugin_snapshot_installed_reload_required'
        backup = Path(result['backup_path'])
        assert (backup / 'plugin/skills/demo/SKILL.md').read_text().endswith('Old.\n')
        registry = json.loads((config / 'plugins/installed_plugins.json').read_text())
        registered = registry['plugins']['example@fixture-market']
        assert len(registered) == 1 and registered[0]['version'] == '1.1.0'
        assert registered[0]['gitCommitSha'] == new_commit
        assert registered[0]['installPath'] == str(cache / '1.1.0')
        assert (cache / '1.1.0/skills/demo/SKILL.md').read_text().endswith('New.\n')
        assert json.loads((backup / 'installed_plugins.json').read_text())['plugins']['example@fixture-market'][0]['version'] == '1.0.0'
        listed = json.loads(run([claude, 'plugin', 'list', '--json']))
        assert len(listed) == 1 and listed[0]['version'] == '1.1.0' and listed[0]['enabled']
        assert not listed[0].get('readFromFolder')
        print('PASS: real Claude owner command, CLI scan/plan/apply, whole-plugin deduplication, '
              'exact commit/content readback, backup, and reload-required result.')
        print('Scope: controlled local Git marketplace snapshot; no remote refresh or running-session reload.')


if __name__ == '__main__':
    main()
