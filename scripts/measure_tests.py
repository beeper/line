#!/usr/bin/env python3
"""Measure cold builds and uncached tests serially on Linux (time, bwrap, /proc)."""
import argparse
import json
import os
from pathlib import Path
import resource
import subprocess
import time


def tree_stats(root):
    processes = {}
    for entry in Path('/proc').iterdir():
        if not entry.name.isdigit():
            continue
        try:
            stat = (entry / 'stat').read_text()
            fields = stat[stat.rfind(')') + 2:].split()
            processes[int(entry.name)] = (int(fields[1]), int(fields[21]) * os.sysconf('SC_PAGE_SIZE'))
        except (OSError, ValueError, IndexError):
            continue
    selected = {root}
    while True:
        expanded = selected | {pid for pid, (parent, _) in processes.items() if parent in selected}
        if expanded == selected:
            break
        selected = expanded
    return sum(processes.get(pid, (0, 0))[1] for pid in selected), len(selected)


def summarize_tests(path):
    counts = {'pass': 0, 'fail': 0, 'skip': 0}
    tests, packages = [], []
    for raw in path.read_text().splitlines():
        try:
            event = json.loads(raw)
        except ValueError:
            continue
        action = event.get('Action')
        if action in counts and event.get('Test'):
            counts[action] += 1
            tests.append({k: event[k] for k in ('Package', 'Test', 'Action', 'Elapsed') if k in event})
        elif action in ('pass', 'fail', 'skip') and event.get('Package'):
            packages.append({k: event[k] for k in ('Package', 'Action', 'Elapsed') if k in event})
    return {'counts': counts, 'packages': sorted(packages, key=lambda x: x.get('Elapsed', 0), reverse=True),
            'tests': sorted(tests, key=lambda x: x.get('Elapsed', 0), reverse=True)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('label', help='New result-directory label; existing results are never overwritten')
    parser.add_argument('--repo', type=Path, default=Path.cwd())
    parser.add_argument('--parallel', type=int, default=2)
    parser.add_argument('--gomaxprocs', type=int, default=4)
    parser.add_argument('--phases', nargs='*')
    args = parser.parse_args()
    if Path(args.label).name != args.label or args.label in ('.', '..'):
        parser.error('label must be a directory name, not a path')
    if args.parallel < 1 or args.gomaxprocs < 1:
        parser.error('parallelism must be positive')
    repo = args.repo.resolve()
    if subprocess.check_output(['git', 'status', '--porcelain'], cwd=repo, text=True).strip():
        parser.error('commit source changes before measuring; the benchmark does not stash or discard work')
    out = repo / '.opencode-work/test-optimization' / args.label
    out.mkdir(parents=True, exist_ok=True)
    env = os.environ.copy()
    env.update(GOMAXPROCS=str(args.gomaxprocs), LC_ALL='C')
    tmp = out / 'tmp'
    tmp.mkdir(exist_ok=True)
    env.update(TMPDIR=str(tmp), GOTMPDIR=str(tmp))
    cache = out / 'cache'
    cache.mkdir(exist_ok=True)
    env['GOCACHE'] = str(cache)
    bins = out / 'bins'
    for mode in ('normal', 'race', 'coverage'):
        (bins / mode).mkdir(parents=True, exist_ok=True)
    meta = {'commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=repo, text=True).strip(),
            'go': subprocess.check_output(['go', 'version'], env=env, text=True).strip(),
            'parallel': args.parallel, 'gomaxprocs': args.gomaxprocs,
            'cpu_count': os.cpu_count(), 'platform': list(os.uname()),
            'environment': {k: env.get(k) for k in ('GOCACHE', 'GOPATH', 'GOTOOLCHAIN', 'GOWORK', 'CGO_ENABLED', 'CGO_CFLAGS', 'CGO_LDFLAGS')},
            'cache_note': 'New per-label build cache; shared prepopulated module download cache. Tests use explicit -count to avoid result caching.',
            'tmp_note': 'bubblewrap redirects historical hard-coded /tmp vector output into this project-local tmp directory.'}
    metadata_path = out / 'environment.json'
    if metadata_path.exists():
        previous = json.loads(metadata_path.read_text())
        for key in ('commit', 'go', 'parallel', 'gomaxprocs'):
            if previous[key] != meta[key]:
                parser.error(f'{key} changed; choose a new result label')
    else:
        metadata_path.write_text(json.dumps(meta, indent=2) + '\n')
    p = str(args.parallel)
    full = ['-p', p, '-count=1', '-timeout=60m', '-json', './...']
    phases = [
        ('cold-normal-build', ['go', 'test', '-p', p, '-c', '-o', str(bins/'normal'), './...']),
        ('warm-normal-build', ['go', 'test', '-p', p, '-c', '-o', str(bins/'normal'), './...']),
        ('normal', ['go', 'test', *full]),
        ('normal-repeat', ['go', 'test', '-p', p, '-count=2', '-shuffle=271', '-timeout=60m', '-json', './...']),
        ('init-normal', ['go', 'test', '-p', p, '-count=1', '-run=^$', '-json', './...']),
        ('profile-signing', [str(bins/'normal/pkg.test'), '-test.count=1', '-test.run=^TestGetSignature', '-test.v', '-test.timeout=60m',
                             '-test.cpuprofile='+str(out/'signing.cpu.pprof'), '-test.memprofile='+str(out/'signing.mem.pprof')]),
        ('cold-race-build', ['go', 'test', '-p', p, '-race', '-c', '-o', str(bins/'race'), './...']),
        ('warm-race-build', ['go', 'test', '-p', p, '-race', '-c', '-o', str(bins/'race'), './...']),
        ('race', ['go', 'test', '-race', *full]),
        ('init-race', ['go', 'test', '-race', '-p', p, '-count=1', '-run=^$', '-json', './...']),
        ('cold-coverage-build', ['go', 'test', '-p', p, '-cover', '-covermode=atomic', '-c', '-o', str(bins/'coverage'), './...']),
        ('coverage', ['go', 'test', '-covermode=atomic', '-coverprofile='+str(out/'coverage.out'), *full]),
        ('cold-normal-suite', ['go', 'test', *full]),
        ('cold-race-suite', ['go', 'test', '-race', *full]),
        ('focus-repeat', ['go', 'test', '-race', '-p', p, '-count=10', '-shuffle=271', '-timeout=60m', '-json',
                          '-run=^(TestGetSignatureEvictsSigningContexts|TestGetLastOpRevisionContextCancellation)$', './pkg', './pkg/line']),
    ]
    valid_phases = {name for name, _ in phases}
    if args.phases and set(args.phases) - valid_phases:
        parser.error('unknown phase: ' + ', '.join(set(args.phases) - valid_phases))
    for name, cmd in phases:
        if args.phases and name not in args.phases:
            continue
        if subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=repo, text=True).strip() != meta['commit'] or subprocess.check_output(['git', 'status', '--porcelain'], cwd=repo, text=True).strip():
            parser.error('source changed during measurement; preserve results and use a new label')
        result_path = out / (name + '.summary.json')
        if result_path.exists():
            if json.loads(result_path.read_text())['exit_status'] != 0:
                parser.error(f'{name} previously failed; preserve it and use a new label for reruns')
            print('Already measured: ' + name, flush=True)
            continue
        cwd = repo / 'pkg' if name == 'profile-signing' else repo
        phase_env = env.copy()
        if name in ('cold-normal-suite', 'cold-race-suite'):
            cold_cache = out / (name + '-cache')
            cold_cache.mkdir(exist_ok=True)
            phase_env['GOCACHE'] = str(cold_cache)
        wrapped = ['bwrap', '--bind', '/', '/', '--dev', '/dev', '--bind', str(tmp), '/tmp', '--chdir', str(cwd), '--', *cmd]
        print('Starting ' + name + ': ' + ' '.join(cmd), flush=True)
        started = time.monotonic()
        load_start = os.getloadavg()
        usage_before = resource.getrusage(resource.RUSAGE_CHILDREN)
        peak_rss, peak_processes = 0, 0
        stdout_path = out / (name + '.jsonl')
        with stdout_path.open('w') as stdout, (out / (name + '.stderr')).open('w') as stderr:
            proc = subprocess.Popen(['/usr/bin/time', '-v', '-o', str(out/(name+'.time.txt')), *wrapped], cwd=repo, env=phase_env, stdout=stdout, stderr=stderr)
            while proc.poll() is None:
                rss, count = tree_stats(proc.pid)
                peak_rss, peak_processes = max(peak_rss, rss), max(peak_processes, count)
                time.sleep(0.1)
        elapsed = time.monotonic() - started
        usage_after = resource.getrusage(resource.RUSAGE_CHILDREN)
        user = usage_after.ru_utime - usage_before.ru_utime
        system = usage_after.ru_stime - usage_before.ru_stime
        result = {'phase': name, 'command': cmd, 'build_cache': phase_env['GOCACHE'], 'exit_status': proc.returncode,
                  'wall_seconds': elapsed, 'user_seconds': user, 'system_seconds': system,
                  'cpu_percent': 100*(user+system)/elapsed, 'peak_tree_rss_bytes': peak_rss,
                  'peak_processes': peak_processes, 'load_average_start': load_start,
                  'load_average_end': os.getloadavg(), **summarize_tests(stdout_path)}
        result_path.write_text(json.dumps(result, indent=2) + '\n')
        print(json.dumps({k: result[k] for k in ('phase', 'exit_status', 'wall_seconds', 'cpu_percent', 'peak_tree_rss_bytes', 'counts')}), flush=True)
        if proc.returncode != 0:
            raise SystemExit(proc.returncode)
    (out/'complete.json').write_text(json.dumps({'completed_at': time.time(), 'commit': meta['commit']}) + '\n')


if __name__ == '__main__':
    main()
