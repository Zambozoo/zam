# Declarations
Declarations serve as scopes or templates from which objects are created.
Executable code is contained within their fields.
Use of declarations is retricted through access modifiers.
Declarations come in seven kinds.

## Namespaces
Namespaces are declarations that are solely for scoping.
They contain static fields only, and their fields are implictly static.
As such, they define no type, and have no instance fields.
They are used in other files through static expressions.
```
// Outer.Zam
namespace Outer {
    public namespace Namespace {
        // static fields
        public var int i;
    }

    // ...
    var int j = Namespace::i;
    // ...
}
```

## Interfaces
Interfaces are strict layouts of definitions that concrete classes must manually implement.
Their fields may be both static and non-static, though they are implicitly non-static.
Abstracts and Classes may implement them.
They are implemented in class and abstract declarations.
Abstract declarations may choose to defer method implementations for clases that extend them.
Interfaces are implicitly implemented, and cannot be explictly inherited.
They are automatically handled by the garbage collector.
```
// Outer.zam
namespace Outer {
    public interface Interface {
        fun void() Fun;
    }

    abstract Abstract {
        abstract fun void() Fun;
    }

    class Class {
        fun void() Fun = () {}
    }

    // ...
    var bool abstractInherits = type(Abstract*) <: type(Interface*); // true
    var bool classInherits = type(Class*) <: type(Interface*); // true
    // ...
}
```

Interfaces may be generic, taking in type parameters for their runtime use.
They may be implemented generically or concretely.
```
// Outer.zam
namespace Outer {
    public interface GenericInterface[A] {
        fun A*() Fun;
    }

    class GenericClass[A] {
        fun A*() Fun = () {
            return null;
        }
    }

    class ConcreteClass {
        fun int*() Fun = () {
            return null;
        }
    }

    // ...
    var bool genericIntInherits = type(GenericClass[int]*) <: type(Interface[int]*); // true
    var bool genericFltInherits = type(GenericClass[flt]*) <: type(Interface[flt]*); // true
    var bool concreteInherits = type(Class*) <: type(Interface[int]*); // true
    // ...
}
```

## Abstracts
Abstracts are declarations that cannot be used to create instances directly.
They are partial templatea for classes to inherit and build upon.
They can be generic, and like interfaces may be inherited generically or concretely.
They are extended explicitly.
Similarly to Interfaces, Abstracts may extended by other Abstracts without fully implementing virtual methods.
They are automatically handled by the garbage collector.
They may be implemented as follows, with all extending classes getting NonVirtualFun for free.
```
// Outer.zam
namespace Outer {
    abstract GenericAbstract[A] {
        virtual fun A*() VirtualFun;
        fun void() NonVirtualFun = () {}
        fun A*() OverrideFun = () {
            var A* a = new(A);
            return a;
        }
    }

    class GenericClass[A] <: GenericAbstract[A]* {
        fun A*() VirtualFun = () {
            return null;
        }
        fun A*() OverrideFun = () {
            return null;
        }
    }

    class ConcreteClass <: GenericAbstract[int]* {
        fun int*() VirtualFun = () {
            return null;
        }
        fun int*() OverrideFun = () {
            return null;
        }
    }

    // ...
    var bool genericIntInherits = type(GenericClass[int]*) <: type(GenericAbstract[int]*); // true
    var bool genericFltInherits = type(GenericClass[flt]*) <: type(GenericAbstract[flt]*); // true
    var bool concreteInherits = type(ConcreteClass*) <: type(GenericAbstract[int]*); // true
    // ...
}
```

## Classes
Classes are declarations that may be used to create instances.
They may extend one another and cannot have virtual methods.
They may also be generic and are automatically handled by the garbage collector.

```
// Outer.zam
namespace Outer {
    abstract GenericClass[A] {
        fun A*() OverrideFun = () {
            var A* a = new(A);
            return a;
        }
    }

    class GenericChildClass[A] <: GenericClass[A]* {
        var A a;
        fun A*() OverrideFun = () {
            return null;
        }
    }

    class ConcreteChildClass <: GenericAbstract[int]* {
        var int i;
        fun int*() OverrideFun = () {
            return null;
        }
    }

    // ...
    var bool genericIntInherits = type(GenericChildClass[int]*) <: type(GenericClass[int]*); // true
    var bool genericFltInherits = type(GenericChildClass[flt]*) <: type(GenericClass[flt]*); // true
    var bool concreteInherits = type(ConcreteChildClass*) <: type(GenericClass[int]*); // true
    // ...
}
```

## Enumerables
Enumerables are declarations that define a fixed set of values.
They are separate from classes, abstracts, and interfaces.
They cannot be generic, and are automatically handled by the garbage collector.

## Structs
# Aliases
