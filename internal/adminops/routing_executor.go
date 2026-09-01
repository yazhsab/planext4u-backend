package adminops

type RoutingExecutor struct {
	fallback Executor
	routes   map[Domain]Executor
}

func NewRoutingExecutor(fallback Executor, routes map[Domain]Executor) (*RoutingExecutor, error) {
	if fallback == nil {
		return nil, ErrInvalidRequest
	}
	cloned := make(map[Domain]Executor, len(routes))
	for domain, executor := range routes {
		if executor == nil {
			return nil, ErrInvalidRequest
		}
		cloned[domain] = executor
	}
	return &RoutingExecutor{fallback: fallback, routes: cloned}, nil
}

func (executor *RoutingExecutor) Execute(principal Principal, change Change) error {
	if routed := executor.routes[change.Command.Domain]; routed != nil {
		return routed.Execute(principal, change)
	}
	return executor.fallback.Execute(principal, change)
}
