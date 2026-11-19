package gha

import "github.com/lukaszraczylo/pipemaster/ir"

const extDefaults = "defaults"

// runDefaults is defaults.run: the shell and working directory of every run step that sets none.
type runDefaults struct {
	shell   string
	workDir string
}

// over returns d with the keys job sets replacing the ones d holds.
func (d runDefaults) over(job runDefaults) runDefaults {
	if job.shell != "" {
		d.shell = job.shell
	}
	if job.workDir != "" {
		d.workDir = job.workDir
	}
	return d
}

// apply sets the defaults on each script step that names no shell or directory of its own.
func (d runDefaults) apply(steps []*ir.Step) {
	for _, st := range steps {
		if st == nil || st.Kind != ir.StepScript {
			continue
		}
		if st.Shell == "" {
			st.Shell = d.shell
		}
		if st.WorkDir == "" {
			st.WorkDir = d.workDir
		}
	}
}

// takeRunDefaults consumes defaults.run; any other key under defaults stays in Ext.
func (fp *fileParser) takeRunDefaults(m *mapping) (runDefaults, error) {
	var d runDefaults
	n := m.take(extDefaults)
	if n == nil {
		return d, nil
	}
	dm, err := fp.mapping(n, extDefaults)
	if err != nil {
		m.release(extDefaults)
		return d, nil
	}
	if rn := dm.take("run"); rn != nil {
		rm, err := fp.mapping(rn, "defaults.run")
		if err != nil {
			dm.release("run")
		} else {
			if v := rm.take("shell"); v != nil {
				if d.shell, err = fp.scalar(v, "defaults.run.shell"); err != nil {
					return d, err
				}
			}
			if v := rm.take("working-directory"); v != nil {
				if d.workDir, err = fp.template(v, "defaults.run.working-directory"); err != nil {
					return d, err
				}
			}
			rest, err := rm.rest()
			if err != nil {
				return d, err
			}
			if rest != nil {
				dm.keep("run", rest)
			}
		}
	}
	rest, err := dm.rest()
	if err != nil {
		return d, err
	}
	if rest != nil {
		m.keep(extDefaults, rest)
	}
	return d, nil
}
