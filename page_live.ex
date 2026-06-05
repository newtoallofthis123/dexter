defmodule Components do
  defmacro __using__(_opts) do
    quote do
      import Components.Baz
    end
  end
end

defmodule Components.Foo do
  def foo do
    ~H"""
    """
  end
end

defmodule Components.Bar do
  def bar do
    ~H"""
    """
  end
end

defmodule Components.Baz do
  def baz do
    ~H"""
    """
  end
end

defmodule Components.Quux do
  use Phoenix.LiveComponent

  def render do
    ~H"""
    """
  end
end

defmodule PageLive do
  alias Components.Foo
  alias Components.Quux

  import Components.Bar

  def render(assigns) do
    ~H"""
    <div>
      <Foo.foo />
      <.bar />
      <.baz />
      <.garply />
      <.live_component module={Quux} id="quux" />
    </div>
    """
  end

  defp garply(assigns) do
    ~H"""
    """
  end
end
