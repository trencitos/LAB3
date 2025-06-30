# Laboratorio 3 - Sistemas Distribuidos

## Grupo 28 🏴‍☠️

| Nombre               | Rol          |
|----------------------|--------------|
| Javiera Barrales     | 202173536-4  |
| Carolina Muñoz       | 202004647-6  |
---------------------------------------

## VM asignadas:
- dist109 --> Jugador1/Servidor1            (IP: 10.35.168.119)  
- dist110 --> Jugador2/Servidor2            (IP: 10.35.168.120)
- dist111 --> Servidor3                     (IP: 10.35.168.121)
- dist112 --> Admin/Matchmaker              (IP: 10.35.168.122)

## ¿Cómo ejecutar?
Se recomienda abrir una terminal separada para cada "VM" para poder observar los logs de cada entidad.
 -> Ubicarse dentro de la carpeta de cada entidad, en caso de las VM: cd LAB3/VMX
        * cd LAB3/VM1/jugador1
        * cd LAB3/VM1/servidor1

        * cd LAB3/VM2/jugador2
        * cd LAB3/VM2/servidor2

        * cd LAB3/VM3/servidor3

        * cd LAB3/VM4/admin
        * cd LAB3/VM4/matchmaker 

 -> Una vez ubicado en la carpeta correspondiente, ORDEN DE EJECUCION:
        make docker-matchmaker
        make docker-servidor1
        make docker-servidor2
        make docker-servidor3
        make docker-admin
        make docker-jugador1
        make docker-jugador2

-> Para detener y eliminar los contenedores: make stop

# Consideraciones
* La entrega en aula y git considera conexiones locales, dentro de las VM están las conexiones con las IP's correspondientes.
* No se implementó la caída de servidores y lo relacionado -> No hay simulación de caída de servidores y, por tanto, el administrador tampoco puede
manejar estos casos. En el código hay implementaciones de cómo se manejarían estas caídas, pero no fueron probadas.
* Se optó por manejar cada entidad en una consola diferente para evitar una sobre-información.
