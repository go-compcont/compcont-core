package compcont

import (
	"fmt"
	"strings"
	"sync"
)

type ComponentContainer struct {
	context         BuildContext
	parent          IComponentContainer
	factoryRegistry IFactoryRegistry
	components      map[ComponentName]Component
	mu              sync.RWMutex
}

// GetSelfComponentName implements IComponentContainer.
func (c *ComponentContainer) GetContext() BuildContext {
	return c.context
}

// GetParent implements IComponentContainer.
func (c *ComponentContainer) GetParent() IComponentContainer {
	return c.parent
}

// GetComponentMetadata implements IComponentContainer.
func (c *ComponentContainer) GetComponent(name ComponentName) (component Component, err error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	inner, ok := c.components[name]
	if !ok {
		err = fmt.Errorf("%w, name: %s", ErrComponentNameNotFound, name)
		return
	}
	component = inner
	return
}

// FactoryRegistry implements IComponentRegistry.
func (c *ComponentContainer) FactoryRegistry() IFactoryRegistry {
	return c.factoryRegistry
}

func (c *ComponentContainer) loadComponent(config ComponentConfig) (component Component, err error) {
	if config.Type == "" {
		if config.Refer == "" { // 引用组件
			err = fmt.Errorf("%w, type && refer are empty, componentName: %s, componentType: %s, refer: %s", ErrComponentConfigInvalid, config.Name, config.Type, config.Refer)
			return
		}
		parts := strings.Split(config.Refer, "/")
		absolute := false
		if parts[0] == "" { // 绝对路径
			absolute = true
			parts = parts[1:]
		}

		var findPath []ComponentName
		for _, p := range parts {

			if n := ComponentName(p); p != "." && p != ".." && !n.Validate() {
				err = fmt.Errorf("%w, in refer %s", ErrComponentNameInvalid, config.Refer)
				return
			} else {
				findPath = append(findPath, n)
			}
		}

		// 寻找到要引用的树节点，再从对应节点上获取组件
		var ctx BuildContext
		ctx, err = find(c, findPath, absolute)
		if err != nil {
			return
		}
		return ctx.Container.GetComponent(ctx.Config.Name)
	}
	// 检查依赖关系是否满足
	for _, dep := range config.Deps {
		if _, ok := c.components[dep]; !ok {
			err = fmt.Errorf("%w, dependency %s not found", ErrComponentDependencyNotFound, dep)
			return
		}
	}

	// 获取工厂
	factory, err := c.factoryRegistry.GetFactory(config.Type)
	if err != nil {
		return
	}

	ctx := BuildContext{
		Config:    config,
		Container: c,
	}

	// 构造组件实例
	instance, err := factory.CreateInstance(ctx, config.Config)
	if err != nil {
		return
	}

	// 构造组件
	component = Component{Instance: instance}
	ctx.Mount = &component
	component.BuildContext = ctx
	return
}

// LoadAnonymousComponent 加载一个匿名组件，返回该组件实例，生命周期不由Registry控制，需要由该方法的调用方自行处理
func (c *ComponentContainer) LoadAnonymousComponent(config ComponentConfig) (component Component, err error) {
	return c.loadComponent(config)
}

// PutComponent implements IComponentContainer.
func (c *ComponentContainer) PutComponent(name ComponentName, component Component) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.components[name] = component
	return
}

// LoadNamedComponents 加载一批具名组件，内部会自行根据拓扑排序顺序加载组件
func (c *ComponentContainer) LoadNamedComponents(configs []ComponentConfig) (err error) {
	// 校验组件名称并构造map
	configMap := make(map[ComponentName]ComponentConfig)
	for _, cfg := range configs {
		if !cfg.Name.Validate() {
			return fmt.Errorf("%w, name: %s, type: %s", ErrComponentNameInvalid, cfg.Name, cfg.Type)
		}
		if _, ok := configMap[cfg.Name]; ok {
			return fmt.Errorf("%w, name: %s", ErrComponentAlreadyExists, cfg.Name)
		}
		configMap[cfg.Name] = cfg
	}

	// 拓扑排序
	var orders []ComponentName
	{
		// 构建组件依赖图
		dag := make(map[ComponentName]set[ComponentName])
		for _, cfg := range configs {
			name := cfg.Name
			if _, ok := dag[name]; !ok {
				dag[name] = make(map[ComponentName]struct{})
			}
			for _, dep := range cfg.Deps {
				// 已存在的依赖关系则不加入本次的DAG构建
				c.mu.RLock()
				_, ok := c.components[dep]
				c.mu.RUnlock()
				if ok {
					continue
				}
				dag[cfg.Name][dep] = struct{}{}
			}
		}

		// 对新组件集合进行拓扑排序
		orders, err = topologicalSort(dag)
		if err != nil {
			return
		}
	}

	for _, name := range orders {
		component, err := c.loadComponent(configMap[name])
		if err != nil {
			return err
		}
		c.mu.Lock()
		c.components[name] = component
		c.mu.Unlock()
	}
	return
}

// UnloadNamedComponents implements IComponentRegistry.
func (c *ComponentContainer) UnloadNamedComponents(name []ComponentName, recursive bool) error {
	panic("unimplemented")
}

// LoadedComponentNames implements IComponentRegistry.
func (c *ComponentContainer) LoadedComponentNames() (names []ComponentName) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// 构建组件依赖图
	dag := make(map[ComponentName]set[ComponentName])
	for _, cfg := range c.components {
		name := cfg.BuildContext.Config.Name
		if _, ok := dag[name]; !ok {
			dag[name] = make(map[ComponentName]struct{})
		}
		for _, dep := range cfg.BuildContext.Config.Deps {
			dag[name][dep] = struct{}{}
		}
	}

	// 对新组件集合进行拓扑排序
	var err error
	names, err = topologicalSort(dag)
	if err != nil {
		panic(fmt.Errorf("topologicalSort error: %w", err))
	}
	return
}

type options struct {
	factoryRegistry IFactoryRegistry
	parent          IComponentContainer
	context         BuildContext
}

type optionsFunc func(o *options)

func WithFactoryRegistry(factoryRegistry IFactoryRegistry) optionsFunc {
	return func(o *options) {
		o.factoryRegistry = factoryRegistry
	}
}

func WithParentContainer(parent IComponentContainer) optionsFunc {
	return func(o *options) {
		o.parent = parent
	}
}

func WithContext(ctx BuildContext) optionsFunc {
	return func(o *options) {
		o.context = ctx
	}
}

func NewComponentContainer(optFns ...optionsFunc) (cr IComponentContainer) {
	var opt options
	for _, fn := range optFns {
		fn(&opt)
	}

	if opt.factoryRegistry == nil {
		opt.factoryRegistry = DefaultFactoryRegistry
	}
	cc := &ComponentContainer{
		factoryRegistry: opt.factoryRegistry,
		parent:          opt.parent,
		components:      make(map[ComponentName]Component),
	}
	comp := &Component{
		Instance: cc,
	}
	cc.context = BuildContext{
		Container: nil,
		Mount:     comp,
	}
	comp.BuildContext = cc.context
	cr = cc
	return
}
